package chat

import (
	"context"
	"strings"
	"time"
)

type Service struct {
	repo *Repository
	hub  *Hub
}

func NewService(repo *Repository, hub *Hub) *Service {
	return &Service{repo: repo, hub: hub}
}

func (s *Service) SearchUsers(ctx context.Context, userID, query string) ([]UserSummary, error) {
	return s.repo.SearchUsers(ctx, userID, query, 20)
}

func (s *Service) ListRooms(ctx context.Context, userID string) ([]Room, error) {
	return s.repo.ListRooms(ctx, userID, 50)
}

func (s *Service) CreateRoom(ctx context.Context, userID, kind, title string, memberIDs []string) (Room, bool, error) {
	switch kind {
	case KindDirect:
		if len(memberIDs) != 1 {
			return Room{}, false, ErrInvalidInput
		}
		room, created, err := s.repo.CreateOrGetDirect(ctx, userID, memberIDs[0])
		if err == nil && created {
			s.hub.Publish(userIDsFromRoom(room), Event{Type: "room.created", RoomID: room.ID, Room: &room})
		}
		return room, created, err
	case KindGroup:
		room, err := s.repo.CreateGroup(ctx, userID, strings.TrimSpace(title), memberIDs)
		if err == nil {
			s.hub.Publish(userIDsFromRoom(room), Event{Type: "room.created", RoomID: room.ID, Room: &room})
		}
		return room, true, err
	default:
		return Room{}, false, ErrInvalidInput
	}
}

func (s *Service) GetRoom(ctx context.Context, userID, roomID string) (Room, error) {
	return s.repo.GetRoom(ctx, userID, roomID)
}

func (s *Service) RenameRoom(ctx context.Context, userID, roomID, title string) (Room, error) {
	room, err := s.repo.RenameRoom(ctx, userID, roomID, title)
	if err == nil {
		s.hub.Publish(userIDsFromRoom(room), Event{Type: "room.updated", RoomID: room.ID, Room: &room})
	}
	return room, err
}

func (s *Service) LeaveRoom(ctx context.Context, userID, roomID string) error {
	room, _ := s.repo.GetRoom(ctx, userID, roomID)
	if err := s.repo.LeaveRoom(ctx, userID, roomID); err != nil {
		return err
	}
	ids := userIDsFromRoom(room)
	s.hub.Publish(ids, Event{Type: "room.updated", RoomID: roomID, UserID: userID})
	return nil
}

func (s *Service) ListMessages(ctx context.Context, userID, roomID string, beforeSeq int64) ([]Message, error) {
	return s.repo.ListMessages(ctx, userID, roomID, beforeSeq, 50)
}

func (s *Service) SendMessage(ctx context.Context, userID, roomID, body string) (Message, error) {
	msg, users, err := s.repo.AppendMessage(ctx, userID, roomID, body)
	if err != nil {
		return Message{}, err
	}
	s.hub.Publish(users, Event{Type: "message.created", RoomID: roomID, Message: &msg})
	return msg, nil
}

// EditMessage updates the body of one of the caller's own messages and fans a
// message.updated event to the room's participants.
func (s *Service) EditMessage(ctx context.Context, userID, roomID, messageID, body string) (Message, error) {
	msg, users, err := s.repo.EditMessage(ctx, userID, roomID, messageID, body)
	if err != nil {
		return Message{}, err
	}
	s.hub.Publish(users, Event{Type: "message.updated", RoomID: roomID, Message: &msg})
	return msg, nil
}

// DeleteMessage soft-deletes one of the caller's own messages and fans a
// message.deleted event so participants' clients drop the row.
func (s *Service) DeleteMessage(ctx context.Context, userID, roomID, messageID string) error {
	users, err := s.repo.DeleteMessage(ctx, userID, roomID, messageID)
	if err != nil {
		return err
	}
	s.hub.Publish(users, Event{
		Type: "message.deleted", RoomID: roomID,
		Message: &Message{ID: messageID, RoomID: roomID},
	})
	return nil
}

func (s *Service) MarkRead(ctx context.Context, userID, roomID string, seq int64) (int64, error) {
	seq, users, err := s.repo.MarkRead(ctx, userID, roomID, seq)
	if err != nil {
		return 0, err
	}
	s.hub.Publish(users, Event{Type: "room.read", RoomID: roomID, UserID: userID, Seq: seq})
	return seq, nil
}

func (s *Service) Subscribe(userID string) (<-chan Event, func()) {
	return s.hub.Subscribe(userID)
}

// ReplayMissed returns up to limit message.created events the user missed
// after sinceTime (across all their rooms), plus the cursor the client should
// store as its new high-water mark. The cursor is the created_at of the last
// replayed message (or the input sinceTime when nothing was missed). The
// cursor is created_at-based — NOT seq — because seq is only unique within a
// single room, so a global seq cursor would silently lose rows from a
// low-activity room. hasMore is true when more rows remain beyond what was
// returned.
func (s *Service) ReplayMissed(ctx context.Context, userID string, since time.Time, limit int) (msgs []Message, cursor time.Time, hasMore bool, err error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	page, err := s.repo.ListMessagesSinceAcrossRooms(ctx, userID, since, limit+1)
	if err != nil {
		return nil, since, false, err
	}
	if len(page) > limit {
		hasMore = true
		page = page[:limit]
	}
	cursor = since
	if len(page) > 0 {
		cursor = page[len(page)-1].CreatedAt.UTC()
	}
	return page, cursor, hasMore, nil
}

func userIDsFromRoom(room Room) []string {
	ids := make([]string, 0, len(room.Participants))
	for _, p := range room.Participants {
		ids = append(ids, p.User.ID)
	}
	return ids
}
