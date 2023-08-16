package room

type EventType = string

type Event interface {
	Type() EventType
}

type UserMeta struct {
	Id        string `json:"id"`
	Streaming bool   `json:"streaming"`
}

type UpdateUsersEvent struct {
	Users []UserMeta `json:"users"`
}

func (e UpdateUsersEvent) Type() EventType {
	return "users"
}
