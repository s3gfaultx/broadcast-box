package room

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
)

var (
	roomMap     map[string]*Room = make(map[string]*Room)
	roomMapLock sync.Mutex
)

var (
	ErrInvalidAuthToken = errors.New("invalid auth token")
)

type Room struct {
	id string

	lock  sync.RWMutex
	users map[UserId]*User
}

// Join adds new user with provided authToken to existing room with given roomId or
// creates new room with given roomId.
func Join(roomId string, authToken string) (*Room, *User, error) {
	if len(authToken) == 0 || len(authToken) >= 1024 {
		return nil, nil, ErrInvalidAuthToken
	}

	roomMapLock.Lock()
	defer roomMapLock.Unlock()

	user := newUser(authToken)

	room, ok := roomMap[roomId]
	if !ok {
		room = newRoom(roomId)
		roomMap[roomId] = room
		log.Printf("Created new room %s by user %s\n", room.id, user.Id)
	} else {
		log.Printf("User %s joined room %s\n", user.Id, room.id)
	}

	room.lock.Lock()
	room.users[user.Id] = user
	room.broadcastUsers()
	room.lock.Unlock()
	return room, user, nil
}

// CloseAll shutdowns gracefully all connections and removes all rooms.
func CloseAll() {
	roomMapLock.Lock()
	defer roomMapLock.Unlock()

	for _, room := range roomMap {
		room.lock.Lock()
		room.close()
		room.lock.Unlock()
	}
}

func newRoom(id string) *Room {
	return &Room{
		id:    id,
		users: make(map[UserId]*User),
	}
}

// RemoveUser removes given user from current room by interrupting all
// active connections like streams user watches or even his own stream.
func (room *Room) RemoveUser(user *User) {
	room.lock.Lock()
	defer room.lock.Unlock()

	log.Printf("Removing user %s from room %s\n", user.Id.String(), room.id)
	room.kickFromStreams(user)
	room.stopStream(user)
	delete(room.users, user.Id)

	if len(room.users) > 0 {
		room.broadcastUsers()
	} else {
		log.Printf("Closing room %s, because all users have left!\n", user.Id.String())
		roomMapLock.Lock()
		room.close()
		delete(roomMap, room.id)
		roomMapLock.Unlock()
	}
}

// kickFromStreams kicks given user from all streams of all users in current room.
func (room *Room) kickFromStreams(user *User) {
	for _, streamer := range room.users {
		stream := streamer.stream.Load().(*userStream)
		if stream != nil {
			stream.lock.Lock()
			if whepSession, ok := stream.viewers[user.Id]; ok {
				_ = whepSession.peerConn.Close()
				delete(stream.viewers, user.Id)
			}
			stream.lock.Unlock()
		}
	}
}

func (room *Room) startStream(user *User, peerConn *webrtc.PeerConnection) (*userStream, error) {
	stream, err := newUserStream(peerConn)
	if err != nil {
		return nil, fmt.Errorf("create user stream: %s", err)
	}

	room.lock.Lock()
	defer room.lock.Unlock()
	if !user.stream.CompareAndSwap((*userStream)(nil), stream) {
		return nil, errors.New("already streaming")
	}
	room.broadcastUsers()
	return stream, nil
}

func (room *Room) stopStream(user *User) {
	if stream := user.stream.Swap((*userStream)(nil)).(*userStream); stream != nil {
		stream.stop()
	}
	room.broadcastUsers()
}

func (room *Room) close() {
	for userId, user := range room.users {
		room.kickFromStreams(user)
		room.stopStream(user)
		close(user.Events)
		delete(room.users, userId)
	}
}

func (room *Room) findByToken(authToken string) *User {
	for _, user := range room.users {
		if user.authToken == authToken {
			return user
		}
	}
	return nil
}

func (room *Room) broadcast(event Event) {
	for _, user := range room.users {
		user.Events <- event
	}
}

func (room *Room) broadcastUsers() {
	users := make([]UserMeta, 0, len(room.users))
	for _, user := range room.users {
		stream := user.stream.Load().(*userStream)
		users = append(users, UserMeta{
			Id:        user.Id.String(),
			Streaming: stream != nil,
		})
	}
	room.broadcast(UpdateUsersEvent{Users: users})
}

type UserId = uuid.UUID

type User struct {
	Id     UserId
	Events chan Event
	// authToken used for WHIP authentication as streamKey and REST api as Authorization header.
	authToken string

	// stream represents active userStream, nil if not currently streaming.
	stream atomic.Value
}

func newUser(authToken string) *User {
	user := &User{
		Id:        uuid.New(),
		Events:    make(chan Event, 32),
		authToken: authToken,
	}
	user.stream.Store((*userStream)(nil))
	return user
}

type userStream struct {
	pliChan  chan any
	peerConn *webrtc.PeerConnection

	lock             sync.RWMutex
	videoTrackLabels []string
	audioTrack       *webrtc.TrackLocalStaticRTP
	viewers          map[UserId]*whepSession
}

func newUserStream(peerConnection *webrtc.PeerConnection) (*userStream, error) {
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		return nil, fmt.Errorf("new track local static rtp: %w", err)
	}
	return &userStream{
		pliChan:          make(chan any, 50),
		peerConn:         peerConnection,
		videoTrackLabels: make([]string, 0, 1),
		audioTrack:       audioTrack,
		viewers:          map[UserId]*whepSession{},
	}, nil
}

func (stream *userStream) addVideoTrack(rid string) error {
	stream.lock.Lock()
	defer stream.lock.Unlock()

	for i := range stream.videoTrackLabels {
		if rid == stream.videoTrackLabels[i] {
			return nil
		}
	}
	stream.videoTrackLabels = append(stream.videoTrackLabels, rid)
	return nil
}

func (stream *userStream) removeViewer(viewer *User) {
	stream.lock.Lock()
	defer stream.lock.Unlock()

	if viewerSession, ok := stream.viewers[viewer.Id]; ok {
		_ = viewerSession.peerConn.Close()
		delete(stream.viewers, viewer.Id)
	}
}

func (stream *userStream) stop() {
	stream.lock.Lock()
	defer stream.lock.Unlock()

	_ = stream.peerConn.Close()
	for viewerId, viewer := range stream.viewers {
		_ = viewer.peerConn.Close()
		delete(stream.viewers, viewerId)
	}
}

// findUserByAuth finds Room and User where user with given authToken is currently located.
func findUserByAuth(authToken string) (*Room, *User) {
	for _, room := range roomMap {
		for _, user := range room.users {
			if user.authToken == authToken {
				return room, user
			}
		}
	}
	return nil, nil
}
