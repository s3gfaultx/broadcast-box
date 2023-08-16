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
	users map[uuid.UUID]*User
}

// JoinRoom adds new user with provided authToken to existing room with given roomId or
// creates new room with given roomId.
func JoinRoom(roomId string, authToken string) (*Room, *User, error) {
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

func newRoom(id string) *Room {
	return &Room{
		id:    id,
		users: make(map[uuid.UUID]*User),
	}
}

func (room *Room) RemoveUser(user *User) {
	log.Printf("Removing user %s from room %s\n", user.Id.String(), room.id)
	room.lock.Lock()
	delete(room.users, user.Id)
	if len(room.users) > 0 {
		room.broadcastUsers()
	} else {
		log.Printf("Closing room %s, because all users have left!\n", user.Id.String())
		roomMapLock.Lock()
		delete(roomMap, room.id)
		roomMapLock.Unlock()
	}
	room.lock.Unlock()
}

func (room *Room) findByToken(authToken string) *User {
	for _, user := range room.users {
		if user.authToken == authToken {
			return user
		}
	}
	return nil
}

// // hasPermission indicates whether user with given authToken has permission to
// // access this room data e.g. streams.
// func (room *Room) hasPermission(authToken string) bool {
// 	return room.findByToken(authToken) != nil
// }

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

type User struct {
	Id     uuid.UUID
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
	pliChan        chan any
	peerConnection *webrtc.PeerConnection

	lock             sync.RWMutex
	videoTrackLabels []string
	audioTrack       *webrtc.TrackLocalStaticRTP
	viewers          map[uuid.UUID]*whepSession
}

func newUserStream(peerConnection *webrtc.PeerConnection) (*userStream, error) {
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		return nil, fmt.Errorf("new track local static rtp: %w", err)
	}
	return &userStream{
		pliChan:          make(chan any, 50),
		peerConnection:   peerConnection,
		videoTrackLabels: make([]string, 0, 1),
		audioTrack:       audioTrack,
		viewers:          map[uuid.UUID]*whepSession{},
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
