package room

type EventType = string

type Event interface {
	Type() EventType
}

type SessionEvent struct {
	SessionId string `json:"sessionId"`
}

func (e SessionEvent) Type() EventType {
	return "session"
}

type UserMeta struct {
	Id        string `json:"id"`
	Streaming bool   `json:"streaming"`
}

type UpdateUsersEvent struct {
	Users []UserMeta `json:"users"`
}

func newUpdateUsersEvent(sessions map[SessionId]*Session) UpdateUsersEvent {
	usersMetas := make([]UserMeta, 0, len(sessions))
	for _, user := range usersFromSessions(sessions) {
		usersMetas = append(usersMetas, UserMeta{
			Id:        user.Id.String(),
			Streaming: user.stream.Load().(*userStream) != nil,
		})
	}
	return UpdateUsersEvent{Users: usersMetas}
}

func (e UpdateUsersEvent) Type() EventType {
	return "users"
}
