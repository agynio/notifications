package server

import (
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
)

const (
	threadParticipantRoomPrefix = "thread_participant:"
	workloadRoomPrefix          = "workload:"
	organizationRoomPrefix      = "organization:"
)

type roomKind int

const (
	roomKindOther roomKind = iota
	roomKindThreadParticipant
	roomKindWorkload
)

type subscriptionRoom struct {
	name string
	kind roomKind
	id   uuid.UUID
}

func parseSubscribeRooms(req *notificationsv1.SubscribeRequest) ([]subscriptionRoom, error) {
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
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		kind, id, err := classifyRoom(trimmed)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "room %d: %v", i, err)
		}
		parsed = append(parsed, subscriptionRoom{name: trimmed, kind: kind, id: id})
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

func classifyRoom(room string) (roomKind, uuid.UUID, error) {
	if id, matched, err := parseRoomUUID(room, threadParticipantRoomPrefix); matched {
		if err != nil {
			return roomKindThreadParticipant, uuid.UUID{}, fmt.Errorf("thread_participant: %w", err)
		}
		return roomKindThreadParticipant, id, nil
	}
	if id, matched, err := parseRoomUUID(room, workloadRoomPrefix); matched {
		if err != nil {
			return roomKindWorkload, uuid.UUID{}, fmt.Errorf("workload: %w", err)
		}
		return roomKindWorkload, id, nil
	}
	return roomKindOther, uuid.UUID{}, nil
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

type WorkloadOrgResolver interface {
	OrgIDForWorkload(workloadID uuid.UUID) (uuid.UUID, bool)
}

type WorkloadOrgRecorder interface {
	RecordEnvelope(envelope *notificationsv1.NotificationEnvelope)
}

type WorkloadOrgIndex struct {
	mu         sync.RWMutex
	byWorkload map[uuid.UUID]uuid.UUID
}

func NewWorkloadOrgIndex() *WorkloadOrgIndex {
	return &WorkloadOrgIndex{byWorkload: make(map[uuid.UUID]uuid.UUID)}
}

func (w *WorkloadOrgIndex) RecordEnvelope(envelope *notificationsv1.NotificationEnvelope) {
	if envelope == nil {
		return
	}
	rooms := envelope.GetRooms()
	if len(rooms) == 0 {
		return
	}
	var (
		workloadID    uuid.UUID
		orgID         uuid.UUID
		foundWorkload bool
		foundOrg      bool
	)
	for _, room := range rooms {
		if id, matched, err := parseRoomUUID(room, workloadRoomPrefix); matched && err == nil {
			workloadID = id
			foundWorkload = true
		}
		if id, matched, err := parseRoomUUID(room, organizationRoomPrefix); matched && err == nil {
			orgID = id
			foundOrg = true
		}
	}
	if !foundWorkload || !foundOrg {
		return
	}
	w.mu.Lock()
	w.byWorkload[workloadID] = orgID
	w.mu.Unlock()
}

func (w *WorkloadOrgIndex) OrgIDForWorkload(workloadID uuid.UUID) (uuid.UUID, bool) {
	w.mu.RLock()
	orgID, ok := w.byWorkload[workloadID]
	w.mu.RUnlock()
	return orgID, ok
}
