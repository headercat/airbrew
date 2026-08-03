package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

var (
	ErrNotFound     = errors.New("chat: not found")
	ErrForbidden    = errors.New("chat: forbidden")
	ErrInvalidInput = errors.New("chat: invalid input")
	ErrConflict     = errors.New("chat: conflict")
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) SearchUsers(ctx context.Context, userID, query string, limit int) ([]UserSummary, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	query = strings.TrimSpace(query)
	args := []any{userID}
	where := "WHERE id != ? AND status = 'active'"
	if query != "" {
		where += " AND (email LIKE ? ESCAPE '\\' OR COALESCE(display_name,'') LIKE ? ESCAPE '\\')"
		p := "%" + escapeLike(query) + "%"
		args = append(args, p, p)
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, email, COALESCE(display_name,''), COALESCE(avatar_url,'')
		  FROM users `+where+`
		 ORDER BY CASE WHEN COALESCE(display_name,'') = '' THEN email ELSE display_name END COLLATE NOCASE
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("chat: search users: %w", err)
	}
	defer rows.Close()
	var out []UserSummary
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Repository) CreateGroup(ctx context.Context, creatorID, title string, memberIDs []string) (Room, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Room{}, fmt.Errorf("%w: title required", ErrInvalidInput)
	}
	ids := uniqueNonEmpty(append(memberIDs, creatorID))
	if len(ids) < 2 {
		return Room{}, fmt.Errorf("%w: at least two participants required", ErrInvalidInput)
	}
	if err := r.ensureActiveUsers(ctx, ids); err != nil {
		return Room{}, err
	}
	roomID := id.New()
	now := time.Now().UTC().Truncate(time.Second)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Room{}, fmt.Errorf("chat: begin create group: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_rooms (id, kind, title, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, KindGroup, title, creatorID, now, now); err != nil {
		return Room{}, fmt.Errorf("chat: insert group: %w", err)
	}
	for _, uid := range ids {
		role := "member"
		if uid == creatorID {
			role = "owner"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_room_participants (room_id, user_id, role, joined_at)
			VALUES (?, ?, ?, ?)`, roomID, uid, role, now); err != nil {
			return Room{}, fmt.Errorf("chat: insert participant: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Room{}, fmt.Errorf("chat: commit create group: %w", err)
	}
	return r.GetRoom(ctx, creatorID, roomID)
}

func (r *Repository) CreateOrGetDirect(ctx context.Context, creatorID, otherUserID string) (Room, bool, error) {
	if creatorID == "" || otherUserID == "" || creatorID == otherUserID {
		return Room{}, false, fmt.Errorf("%w: direct chat needs another user", ErrInvalidInput)
	}
	if err := r.ensureActiveUsers(ctx, []string{creatorID, otherUserID}); err != nil {
		return Room{}, false, err
	}
	low, high := directKey(creatorID, otherUserID)
	var existing string
	err := r.db.QueryRowContext(ctx,
		"SELECT room_id FROM chat_direct_room_keys WHERE user_low_id = ? AND user_high_id = ?",
		low, high,
	).Scan(&existing)
	if err == nil {
		room, err := r.GetRoom(ctx, creatorID, existing)
		return room, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Room{}, false, fmt.Errorf("chat: lookup direct: %w", err)
	}

	roomID := id.New()
	now := time.Now().UTC().Truncate(time.Second)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Room{}, false, fmt.Errorf("chat: begin direct: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_rooms (id, kind, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		roomID, KindDirect, creatorID, now, now); err != nil {
		return Room{}, false, fmt.Errorf("chat: insert direct: %w", err)
	}
	for _, uid := range []string{creatorID, otherUserID} {
		role := "member"
		if uid == creatorID {
			role = "owner"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_room_participants (room_id, user_id, role, joined_at)
			VALUES (?, ?, ?, ?)`, roomID, uid, role, now); err != nil {
			return Room{}, false, fmt.Errorf("chat: insert direct participant: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_direct_room_keys (user_low_id, user_high_id, room_id)
		VALUES (?, ?, ?)`, low, high, roomID); err != nil {
		return Room{}, false, fmt.Errorf("chat: insert direct key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Room{}, false, fmt.Errorf("chat: commit direct: %w", err)
	}
	room, err := r.GetRoom(ctx, creatorID, roomID)
	return room, true, err
}

func (r *Repository) ListRooms(ctx context.Context, userID string, limit int) ([]Room, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT cr.id, cr.kind, COALESCE(cr.title,''), cr.created_by, cr.created_at, cr.updated_at,
		       crp.last_read_seq,
		       COALESCE((SELECT COUNT(*) FROM chat_messages cm
		                  WHERE cm.room_id = cr.id AND cm.seq > crp.last_read_seq
		                    AND cm.sender_id != ? AND cm.deleted_at IS NULL), 0)
		  FROM chat_rooms cr
		  JOIN chat_room_participants crp ON crp.room_id = cr.id AND crp.user_id = ?
		 WHERE cr.deleted_at IS NULL
		 ORDER BY cr.updated_at DESC
		 LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("chat: list rooms: %w", err)
	}
	defer rows.Close()
	var rooms []Room
	for rows.Next() {
		var room Room
		if err := rows.Scan(&room.ID, &room.Kind, &room.Title, &room.CreatedBy, &room.CreatedAt, &room.UpdatedAt, &room.LastReadSeq, &room.UnreadCount); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range rooms {
		parts, err := r.listParticipants(ctx, rooms[i].ID)
		if err != nil {
			return nil, err
		}
		rooms[i].Participants = parts
		msg, err := r.lastMessage(ctx, rooms[i].ID)
		if err != nil {
			return nil, err
		}
		rooms[i].LastMessage = msg
		rooms[i].Title = roomTitle(rooms[i], userID)
	}
	return rooms, nil
}

func (r *Repository) GetRoom(ctx context.Context, userID, roomID string) (Room, error) {
	var room Room
	err := r.db.QueryRowContext(ctx, `
		SELECT cr.id, cr.kind, COALESCE(cr.title,''), cr.created_by, cr.created_at, cr.updated_at,
		       crp.last_read_seq,
		       COALESCE((SELECT COUNT(*) FROM chat_messages cm
		                  WHERE cm.room_id = cr.id AND cm.seq > crp.last_read_seq
		                    AND cm.sender_id != ? AND cm.deleted_at IS NULL), 0)
		  FROM chat_rooms cr
		  JOIN chat_room_participants crp ON crp.room_id = cr.id AND crp.user_id = ?
		 WHERE cr.id = ? AND cr.deleted_at IS NULL`,
		userID, userID, roomID,
	).Scan(&room.ID, &room.Kind, &room.Title, &room.CreatedBy, &room.CreatedAt, &room.UpdatedAt, &room.LastReadSeq, &room.UnreadCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	if err != nil {
		return Room{}, fmt.Errorf("chat: get room: %w", err)
	}
	parts, err := r.listParticipants(ctx, room.ID)
	if err != nil {
		return Room{}, err
	}
	room.Participants = parts
	msg, err := r.lastMessage(ctx, room.ID)
	if err != nil {
		return Room{}, err
	}
	room.LastMessage = msg
	room.Title = roomTitle(room, userID)
	return room, nil
}

func (r *Repository) RenameRoom(ctx context.Context, userID, roomID, title string) (Room, error) {
	if strings.TrimSpace(title) == "" {
		return Room{}, fmt.Errorf("%w: title required", ErrInvalidInput)
	}
	role, kind, err := r.participantRole(ctx, userID, roomID)
	if err != nil {
		return Room{}, err
	}
	if kind != KindGroup || role != "owner" {
		return Room{}, ErrForbidden
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx,
		"UPDATE chat_rooms SET title = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL",
		strings.TrimSpace(title), now, roomID,
	)
	if err != nil {
		return Room{}, fmt.Errorf("chat: rename room: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Room{}, ErrNotFound
	}
	return r.GetRoom(ctx, userID, roomID)
}

func (r *Repository) LeaveRoom(ctx context.Context, userID, roomID string) error {
	_, kind, err := r.participantRole(ctx, userID, roomID)
	if err != nil {
		return err
	}
	if kind == KindDirect {
		return ErrForbidden
	}
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM chat_room_participants WHERE room_id = ? AND user_id = ?",
		roomID, userID,
	)
	if err != nil {
		return fmt.Errorf("chat: leave room: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ListMessages(ctx context.Context, userID, roomID string, beforeSeq int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return nil, err
	}
	where := "WHERE cm.room_id = ? AND cm.deleted_at IS NULL"
	args := []any{roomID}
	if beforeSeq > 0 {
		where += " AND cm.seq < ?"
		args = append(args, beforeSeq)
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, messageSelect()+`
		`+where+`
		ORDER BY cm.seq DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("chat: list messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListMessagesSinceAcrossRooms returns up to limit non-deleted messages with
// seq > sinceSeq across every room the user participates in, ordered by seq.
// It backs the SSE reconnect replay so a dropped event (subscriber buffer
// full) or a transient disconnect does not permanently lose a message.
func (r *Repository) ListMessagesSinceAcrossRooms(ctx context.Context, userID string, sinceSeq int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, messageSelect()+`
		JOIN chat_room_participants p
		  ON p.room_id = cm.room_id AND p.user_id = ?
		WHERE p.user_id = ?
		  AND cm.deleted_at IS NULL
		  AND cm.seq > ?
		ORDER BY cm.seq ASC
		LIMIT ?`,
		userID, userID, sinceSeq, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("chat: replay messages since: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		msg, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// MaxSeqAcrossRooms returns the highest chat_messages.seq the user can see, or
// 0 when they have none. Used to seed the SSE replay cursor.
func (r *Repository) MaxSeqAcrossRooms(ctx context.Context, userID string) (int64, error) {
	var seq sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT MAX(cm.seq)
		FROM chat_messages cm
		JOIN chat_room_participants p
		  ON p.room_id = cm.room_id AND p.user_id = ?
		WHERE p.user_id = ? AND cm.deleted_at IS NULL`,
		userID, userID,
	).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// maxMessageBody is the byte cap enforced on both send and edit so an edited
// message cannot bypass the send-time limit.
const maxMessageBody = 8000

func (r *Repository) AppendMessage(ctx context.Context, userID, roomID, body string) (Message, []string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, nil, fmt.Errorf("%w: message required", ErrInvalidInput)
	}
	if len(body) > maxMessageBody {
		return Message{}, nil, fmt.Errorf("%w: message too long", ErrInvalidInput)
	}
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return Message{}, nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	msgID := id.New()
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return Message{}, nil, fmt.Errorf("chat: begin append: %w", err)
		}
		var seq int64
		if err := tx.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(seq), 0) + 1 FROM chat_messages WHERE room_id = ?",
			roomID,
		).Scan(&seq); err != nil {
			_ = tx.Rollback()
			return Message{}, nil, fmt.Errorf("chat: next seq: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_messages (id, room_id, sender_id, seq, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			msgID, roomID, userID, seq, body, now); err != nil {
			_ = tx.Rollback()
			if isUniqueErr(err) {
				continue
			}
			return Message{}, nil, fmt.Errorf("chat: insert message: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_rooms SET updated_at = ? WHERE id = ?`, now, roomID); err != nil {
			_ = tx.Rollback()
			return Message{}, nil, fmt.Errorf("chat: touch room: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_room_participants SET last_read_seq = MAX(last_read_seq, ?) WHERE room_id = ? AND user_id = ?`,
			seq, roomID, userID); err != nil {
			_ = tx.Rollback()
			return Message{}, nil, fmt.Errorf("chat: mark sender read: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return Message{}, nil, fmt.Errorf("chat: commit append: %w", err)
		}
		msg, err := r.GetMessage(ctx, userID, roomID, msgID)
		if err != nil {
			return Message{}, nil, err
		}
		recipients, err := r.RoomUserIDs(ctx, roomID)
		return msg, recipients, err
	}
	return Message{}, nil, ErrConflict
}

func (r *Repository) GetMessage(ctx context.Context, userID, roomID, messageID string) (Message, error) {
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return Message{}, err
	}
	row := r.db.QueryRowContext(ctx, messageSelect()+`
		WHERE cm.room_id = ? AND cm.id = ? AND cm.deleted_at IS NULL`, roomID, messageID)
	return scanMessage(row)
}

// EditMessage updates the body of one of the caller's own messages and stamps
// edited_at. Only the original sender may edit; other participants get
// ErrForbidden. Returns the edited message and the room's recipient ids so the
// service can broadcast a message.updated event.
func (r *Repository) EditMessage(ctx context.Context, userID, roomID, messageID, body string) (Message, []string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, nil, fmt.Errorf("%w: message required", ErrInvalidInput)
	}
	if len(body) > maxMessageBody {
		return Message{}, nil, fmt.Errorf("%w: message too long", ErrInvalidInput)
	}
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return Message{}, nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages
		   SET body = ?, edited_at = ?
		 WHERE id = ? AND room_id = ? AND sender_id = ? AND deleted_at IS NULL`,
		body, now, messageID, roomID, userID,
	)
	if err != nil {
		return Message{}, nil, fmt.Errorf("chat: edit message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Distinguish "not found / deleted" from "not the sender" by checking
		// the row's existence, so the caller maps to 404 vs 403.
		var owner string
		err := r.db.QueryRowContext(ctx,
			`SELECT sender_id FROM chat_messages WHERE id = ? AND room_id = ?`,
			messageID, roomID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, nil, ErrNotFound
		}
		if err == nil && owner != userID {
			return Message{}, nil, ErrForbidden
		}
		return Message{}, nil, ErrNotFound
	}
	// Keep the room's ordering fresh so the sidebar reflects the edit.
	if _, err := r.db.ExecContext(ctx,
		`UPDATE chat_rooms SET updated_at = ? WHERE id = ?`, now, roomID); err != nil {
		return Message{}, nil, fmt.Errorf("chat: touch room on edit: %w", err)
	}
	msg, err := r.GetMessage(ctx, userID, roomID, messageID)
	if err != nil {
		return Message{}, nil, err
	}
	recipients, err := r.RoomUserIDs(ctx, roomID)
	return msg, recipients, err
}

// DeleteMessage soft-deletes one of the caller's own messages (sets
// deleted_at). Deleted rows are excluded by ListMessages/GetMessage. Returns
// the recipient ids so the service can broadcast message.deleted.
func (r *Repository) DeleteMessage(ctx context.Context, userID, roomID, messageID string) ([]string, error) {
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages SET deleted_at = ?
		 WHERE id = ? AND room_id = ? AND sender_id = ? AND deleted_at IS NULL`,
		now, messageID, roomID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("chat: delete message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var owner string
		qerr := r.db.QueryRowContext(ctx,
			`SELECT sender_id FROM chat_messages WHERE id = ? AND room_id = ?`,
			messageID, roomID).Scan(&owner)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if qerr == nil && owner != userID {
			return nil, ErrForbidden
		}
		return nil, ErrNotFound
	}
	return r.RoomUserIDs(ctx, roomID)
}

func (r *Repository) MarkRead(ctx context.Context, userID, roomID string, seq int64) (int64, []string, error) {
	if seq < 0 {
		return 0, nil, fmt.Errorf("%w: invalid read seq", ErrInvalidInput)
	}
	if _, _, err := r.participantRole(ctx, userID, roomID); err != nil {
		return 0, nil, err
	}
	if seq == 0 {
		_ = r.db.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(seq),0) FROM chat_messages WHERE room_id = ? AND deleted_at IS NULL",
			roomID,
		).Scan(&seq)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_room_participants
		   SET last_read_seq = MAX(last_read_seq, ?)
		 WHERE room_id = ? AND user_id = ?`,
		seq, roomID, userID,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("chat: mark read: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, nil, ErrNotFound
	}
	users, err := r.RoomUserIDs(ctx, roomID)
	return seq, users, err
}

func (r *Repository) RoomUserIDs(ctx context.Context, roomID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT user_id FROM chat_room_participants WHERE room_id = ?",
		roomID,
	)
	if err != nil {
		return nil, fmt.Errorf("chat: room users: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	return ids, rows.Err()
}

func (r *Repository) ensureActiveUsers(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: participants required", ErrInvalidInput)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, uid := range ids {
		args = append(args, uid)
	}
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE status = 'active' AND id IN ("+placeholders+")",
		args...,
	).Scan(&n); err != nil {
		return fmt.Errorf("chat: check users: %w", err)
	}
	if n != len(ids) {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) participantRole(ctx context.Context, userID, roomID string) (string, string, error) {
	var role, kind string
	err := r.db.QueryRowContext(ctx, `
		SELECT crp.role, cr.kind
		  FROM chat_room_participants crp
		  JOIN chat_rooms cr ON cr.id = crp.room_id AND cr.deleted_at IS NULL
		 WHERE crp.room_id = ? AND crp.user_id = ?`,
		roomID, userID,
	).Scan(&role, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("chat: participant role: %w", err)
	}
	return role, kind, nil
}

func (r *Repository) listParticipants(ctx context.Context, roomID string) ([]Participant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.id, u.email, COALESCE(u.display_name,''), COALESCE(u.avatar_url,''),
		       crp.role, crp.joined_at, crp.last_read_seq
		  FROM chat_room_participants crp
		  JOIN users u ON u.id = crp.user_id
		 WHERE crp.room_id = ?
		 ORDER BY CASE crp.role WHEN 'owner' THEN 0 ELSE 1 END,
		          CASE WHEN COALESCE(u.display_name,'') = '' THEN u.email ELSE u.display_name END COLLATE NOCASE`,
		roomID,
	)
	if err != nil {
		return nil, fmt.Errorf("chat: list participants: %w", err)
	}
	defer rows.Close()
	var out []Participant
	for rows.Next() {
		var p Participant
		if err := rows.Scan(&p.User.ID, &p.User.Email, &p.User.DisplayName, &p.User.AvatarURL, &p.Role, &p.JoinedAt, &p.LastReadSeq); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) lastMessage(ctx context.Context, roomID string) (*Message, error) {
	row := r.db.QueryRowContext(ctx, messageSelect()+`
		WHERE cm.room_id = ? AND cm.deleted_at IS NULL
		ORDER BY cm.seq DESC LIMIT 1`, roomID)
	msg, err := scanMessage(row)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func messageSelect() string {
	return `
		SELECT cm.id, cm.room_id, cm.sender_id, u.email, COALESCE(u.display_name,''),
		       COALESCE(u.avatar_url,''), cm.seq, cm.body, cm.created_at,
		       cm.edited_at, cm.deleted_at,
		       COALESCE((SELECT COUNT(*) FROM chat_room_participants crp
		                  WHERE crp.room_id = cm.room_id AND crp.last_read_seq >= cm.seq), 0)
		  FROM chat_messages cm
		  JOIN users u ON u.id = cm.sender_id `
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(row scanner) (Message, error) {
	var msg Message
	var edited, deleted sql.NullTime
	if err := row.Scan(
		&msg.ID, &msg.RoomID, &msg.SenderID, &msg.Sender.Email, &msg.Sender.DisplayName,
		&msg.Sender.AvatarURL, &msg.Seq, &msg.Body, &msg.CreatedAt,
		&edited, &deleted, &msg.ReadByCount,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, ErrNotFound
		}
		return Message{}, err
	}
	msg.Sender.ID = msg.SenderID
	if edited.Valid {
		msg.EditedAt = &edited.Time
	}
	if deleted.Valid {
		msg.DeletedAt = &deleted.Time
	}
	return msg, nil
}

func directKey(a, b string) (string, string) {
	ids := []string{a, b}
	sort.Strings(ids)
	return ids[0], ids[1]
}

func uniqueNonEmpty(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, uid := range ids {
		uid = strings.TrimSpace(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}

func roomTitle(room Room, viewerID string) string {
	if room.Kind == KindGroup {
		return room.Title
	}
	for _, p := range room.Participants {
		if p.User.ID != viewerID {
			if p.User.DisplayName != "" {
				return p.User.DisplayName
			}
			return p.User.Email
		}
	}
	return "Direct message"
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func isUniqueErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique")
}
