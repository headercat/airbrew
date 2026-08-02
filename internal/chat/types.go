package chat

import "time"

const (
	KindDirect = "direct"
	KindGroup  = "group"
)

type UserSummary struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
}

type Participant struct {
	User        UserSummary `json:"user"`
	Role        string      `json:"role"`
	JoinedAt    time.Time   `json:"joined_at"`
	LastReadSeq int64       `json:"last_read_seq"`
}

type Room struct {
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Title        string        `json:"title"`
	CreatedBy    string        `json:"created_by"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Participants []Participant `json:"participants,omitempty"`
	LastMessage  *Message      `json:"last_message,omitempty"`
	LastReadSeq  int64         `json:"last_read_seq"`
	UnreadCount  int64         `json:"unread_count"`
}

type Message struct {
	ID          string      `json:"id"`
	RoomID      string      `json:"room_id"`
	Sender      UserSummary `json:"sender"`
	SenderID    string      `json:"sender_id"`
	Seq         int64       `json:"seq"`
	Body        string      `json:"body"`
	CreatedAt   time.Time   `json:"created_at"`
	EditedAt    *time.Time  `json:"edited_at,omitempty"`
	DeletedAt   *time.Time  `json:"deleted_at,omitempty"`
	ReadByCount int64       `json:"read_by_count,omitempty"`
}

type Event struct {
	Type    string   `json:"type"`
	RoomID  string   `json:"room_id,omitempty"`
	Room    *Room    `json:"room,omitempty"`
	Message *Message `json:"message,omitempty"`
	UserID  string   `json:"user_id,omitempty"`
	Seq     int64    `json:"seq,omitempty"`
}
