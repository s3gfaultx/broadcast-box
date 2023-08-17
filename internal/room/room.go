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

	lock     sync.RWMutex
	sessions map[SessionId]*Session
}

// Join adds new user with provided authToken to existing room with given roomId or
// creates new room with given roomId.
func Join(roomId string, authToken string) (*Room, *Session, error) {
	if len(authToken) == 0 || len(authToken) >= 1024 {
		return nil, nil, ErrInvalidAuthToken
	}

	roomMapLock.Lock()
	defer roomMapLock.Unlock()

	room, ok := roomMap[roomId]
	if !ok {
		room = newRoom(roomId)
		roomMap[roomId] = room
	}
	room.lock.Lock()
	defer room.lock.Unlock()

	var user *User
	createdUser := false
	if session := room.sessionByAuth(authToken); session != nil {
		user = session.User
	} else {
		user = newUser(authToken)
		createdUser = true
		log.Printf("New user %s joined room %s\n", user.Id, room.id)
	}
	session := &Session{
		Id:     uuid.New(),
		Events: make(chan Event, 32),
		User:   user,
	}
	room.sessions[session.Id] = session
	session.Events <- SessionEvent{SessionId: session.Id.String()}
	if createdUser {
		room.broadcastUsers()
	} else {
		session.Events <- newUpdateUsersEvent(room.sessions)
	}
	return room, session, nil
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
		id:       id,
		sessions: make(map[SessionId]*Session, 0),
	}
}

// // RemoveSession removes given user from current room by interrupting all
// // active connections like streams user watches or even his own stream.
func (room *Room) RemoveSession(session *Session) {
	room.lock.Lock()
	defer room.lock.Unlock()

	room.kickFromStreams(session)
	delete(room.sessions, session.Id)
	if room.user(session.User.Id) != nil {
		log.Printf("Session %s has quit from room %s\n", session.Id.String(), room.id)
		return
	}

	user := session.User
	log.Printf("Removing user %s from room %s\n", user.Id.String(), room.id)
	room.stopStream(user)

	if len(room.sessions) > 0 {
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
func (room *Room) kickFromStreams(session *Session) {
	for _, streamerSession := range room.sessions {
		stream := streamerSession.User.stream.Load().(*userStream)
		if stream != nil {
			stream.lock.Lock()
			if whepSession, ok := stream.viewers[session.Id]; ok {
				_ = whepSession.peerConn.Close()
				delete(stream.viewers, session.Id)
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
	for sessionId, session := range room.sessions {
		room.kickFromStreams(session)
		room.stopStream(session.User)
		close(session.Events)
		delete(room.sessions, sessionId)
	}
}

func (room *Room) User(userId UserId) *User {
	room.lock.Lock()
	defer room.lock.Unlock()
	return room.user(userId)
}

func (room *Room) user(userId UserId) *User {
	for _, session := range room.sessions {
		if session.User.Id == userId {
			return session.User
		}
	}
	return nil
}

func (room *Room) sessionByAuth(authToken string) *Session {
	for _, session := range room.sessions {
		if session.User.AuthToken == authToken {
			return session
		}
	}
	return nil
}

func (room *Room) broadcast(event Event) {
	for _, session := range room.sessions {
		session.Events <- event
	}
}

func (room *Room) broadcastUsers() {
	room.broadcast(newUpdateUsersEvent(room.sessions))
}

type SessionId = uuid.UUID

type Session struct {
	Id     SessionId
	Events chan Event
	User   *User
}

type UserId = uuid.UUID

// User represents one or more active connections to Room from unique AuthToken.
type User struct {
	Id UserId
	// AuthToken used for WHIP authentication as streamKey and REST api as Authorization header.
	AuthToken string

	// stream represents active userStream, nil if not currently streaming.
	stream atomic.Value
}

func newUser(authToken string) *User {
	user := &User{
		Id:        uuid.New(),
		AuthToken: authToken,
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
	viewers          map[SessionId]*whepSession
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

func (stream *userStream) removeViewer(viewer *Session) {
	stream.lock.Lock()
	defer stream.lock.Unlock()

	if viewerSession, ok := stream.viewers[viewer.Id]; ok {
		viewerSession.close()
		delete(stream.viewers, viewer.Id)
	}
}

func (stream *userStream) stop() {
	stream.lock.Lock()
	defer stream.lock.Unlock()

	_ = stream.peerConn.Close()
	for viewerId, viewer := range stream.viewers {
		viewer.close()
		delete(stream.viewers, viewerId)
	}
}

func FindSession(sessionId SessionId) (*Room, *Session) {
	roomMapLock.Lock()
	defer roomMapLock.Unlock()

	for _, room := range roomMap {
		for _, session := range room.sessions {
			if session.Id == sessionId {
				return room, session
			}
		}
	}
	return nil, nil
}

// findUserByAuth finds Room and User where user with given authToken is currently located.
func findUserByAuth(authToken string) (*Room, *User) {
	for _, room := range roomMap {
		for _, user := range usersFromSessions(room.sessions) {
			if user.AuthToken == authToken {
				return room, user
			}
		}
	}
	return nil, nil
}

func usersFromSessions(sessions map[SessionId]*Session) map[UserId]*User {
	users := make(map[uuid.UUID]*User, 0)
	for _, session := range sessions {
		users[session.User.Id] = session.User
	}
	return users
}
