package server

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

const (
	threadParticipantRoomPrefix = "thread_participant:"
	instanceInboxRoomPrefix     = "instance_inbox:"
	workloadRoomPrefix          = "workload:"
	organizationRoomPrefix      = "organization:"
	agentRoomPrefix             = "agent:"
	traceRoomPrefix             = "trace:"
	selfRoomIDSegment           = "me"
)

type roomKind int

const (
	roomKindOther roomKind = iota
	roomKindThreadParticipant
	roomKindInstanceInbox
	roomKindWorkload
	roomKindOrganization
	roomKindAgent
	roomKindTrace
)

type subscriptionRoom struct {
	name    string
	kind    roomKind
	id      uuid.UUID
	traceID string
}

func parseSubscribeRooms(req *notificationsv1.SubscribeRequest, callerID uuid.UUID) ([]subscriptionRoom, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	rooms := req.GetRooms()
	if len(rooms) == 0 {
		return nil, status.Error(codes.InvalidArgument, "rooms required")
	}
	seen := make(map[string]struct{}, len(rooms))
	parsed := make([]subscriptionRoom, 0, len(rooms))
	for i, room := range rooms {
		trimmed := strings.TrimSpace(room)
		if trimmed == "" {
			return nil, status.Errorf(codes.InvalidArgument, "room %d is empty", i)
		}
		kind, id, traceID, canonical, err := classifyRoom(trimmed, callerID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "room %d: %v", i, err)
		}
		name := trimmed
		if canonical != "" {
			name = canonical
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		parsed = append(parsed, subscriptionRoom{name: name, kind: kind, id: id, traceID: traceID})
	}
	if len(parsed) == 0 {
		return nil, status.Error(codes.InvalidArgument, "rooms required")
	}
	return parsed, nil
}

func roomNames(rooms []subscriptionRoom) []string {
	if len(rooms) == 0 {
		return nil
	}
	values := make([]string, len(rooms))
	for i, room := range rooms {
		values[i] = room.name
	}
	return values
}

func classifyRoom(room string, callerID uuid.UUID) (roomKind, uuid.UUID, string, string, error) {
	if strings.HasPrefix(room, threadParticipantRoomPrefix) {
		raw := strings.TrimSpace(strings.TrimPrefix(room, threadParticipantRoomPrefix))
		if raw == selfRoomIDSegment {
			if callerID == uuid.Nil {
				return roomKindThreadParticipant, uuid.UUID{}, "", "", fmt.Errorf("thread_participant: caller identity is required for %q", selfRoomIDSegment)
			}
			return roomKindThreadParticipant, callerID, "", threadParticipantRoomPrefix + callerID.String(), nil
		}
		id, err := parseUUID(raw)
		if err != nil {
			return roomKindThreadParticipant, uuid.UUID{}, "", "", fmt.Errorf("thread_participant: %w", err)
		}
		return roomKindThreadParticipant, id, "", threadParticipantRoomPrefix + id.String(), nil
	}
	if strings.HasPrefix(room, instanceInboxRoomPrefix) {
		raw := strings.TrimSpace(strings.TrimPrefix(room, instanceInboxRoomPrefix))
		if raw == selfRoomIDSegment {
			if callerID == uuid.Nil {
				return roomKindInstanceInbox, uuid.UUID{}, "", "", fmt.Errorf("instance_inbox: caller identity is required for %q", selfRoomIDSegment)
			}
			return roomKindInstanceInbox, callerID, "", instanceInboxRoomPrefix + callerID.String(), nil
		}
		id, err := parseUUID(raw)
		if err != nil {
			return roomKindInstanceInbox, uuid.UUID{}, "", "", fmt.Errorf("instance_inbox: %w", err)
		}
		return roomKindInstanceInbox, id, "", instanceInboxRoomPrefix + id.String(), nil
	}
	if id, matched, err := parseRoomUUID(room, workloadRoomPrefix); matched {
		if err != nil {
			return roomKindWorkload, uuid.UUID{}, "", "", fmt.Errorf("workload: %w", err)
		}
		return roomKindWorkload, id, "", workloadRoomPrefix + id.String(), nil
	}
	if id, matched, err := parseRoomUUID(room, organizationRoomPrefix); matched {
		if err != nil {
			return roomKindOrganization, uuid.UUID{}, "", "", fmt.Errorf("organization: %w", err)
		}
		return roomKindOrganization, id, "", organizationRoomPrefix + id.String(), nil
	}
	if id, matched, err := parseRoomUUID(room, agentRoomPrefix); matched {
		if err != nil {
			return roomKindAgent, uuid.UUID{}, "", "", fmt.Errorf("agent: %w", err)
		}
		return roomKindAgent, id, "", agentRoomPrefix + id.String(), nil
	}
	if traceID, matched, err := parseRoomTraceID(room); matched {
		if err != nil {
			return roomKindTrace, uuid.UUID{}, "", "", fmt.Errorf("trace: %w", err)
		}
		return roomKindTrace, uuid.UUID{}, traceID, traceRoomPrefix + traceID, nil
	}
	return roomKindOther, uuid.UUID{}, "", "", nil
}

func parseRoomUUID(room string, prefix string) (uuid.UUID, bool, error) {
	if !strings.HasPrefix(room, prefix) {
		return uuid.UUID{}, false, nil
	}
	id, err := parseUUID(strings.TrimPrefix(room, prefix))
	if err != nil {
		return uuid.UUID{}, true, err
	}
	return id, true, nil
}

func parseUUID(value string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return uuid.UUID{}, fmt.Errorf("value is empty")
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return uuid.UUID{}, err
	}
	return parsed, nil
}

func parseRoomTraceID(room string) (string, bool, error) {
	if !strings.HasPrefix(room, traceRoomPrefix) {
		return "", false, nil
	}
	raw := strings.TrimSpace(strings.TrimPrefix(room, traceRoomPrefix))
	if raw == "" {
		return "", true, fmt.Errorf("trace id is empty")
	}
	if len(raw) != 32 {
		return "", true, fmt.Errorf("trace id must be 32 hex characters")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", true, err
	}
	return strings.ToLower(raw), true, nil
}
