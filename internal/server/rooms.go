package server

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notificationsv1 "github.com/agynio/notifications/internal/.gen/agynio/api/notifications/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	threadParticipantRoomPrefix = "thread_participant:"
	workloadRoomPrefix          = "workload:"
	organizationRoomPrefix      = "organization:"
	traceRoomPrefix             = "trace:"
)

type roomKind int

const (
	roomKindOther roomKind = iota
	roomKindThreadParticipant
	roomKindWorkload
	roomKindOrganization
	roomKindTrace
)

type subscriptionRoom struct {
	name    string
	kind    roomKind
	id      uuid.UUID
	traceID string
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
		kind, id, traceID, canonical, err := classifyRoom(trimmed)
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

func classifyRoom(room string) (roomKind, uuid.UUID, string, string, error) {
	if id, matched, err := parseRoomUUID(room, threadParticipantRoomPrefix); matched {
		if err != nil {
			return roomKindThreadParticipant, uuid.UUID{}, "", "", fmt.Errorf("thread_participant: %w", err)
		}
		return roomKindThreadParticipant, id, "", threadParticipantRoomPrefix + id.String(), nil
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

type WorkloadOrgResolver interface {
	OrgIDForWorkload(workloadID uuid.UUID) (uuid.UUID, bool)
}

type WorkloadOrgRecorder interface {
	RecordEnvelope(envelope *notificationsv1.NotificationEnvelope)
}

type TraceOrgResolver interface {
	OrgIDForTrace(traceID string) (uuid.UUID, bool)
}

type TraceOrgRecorder interface {
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

type TraceOrgIndex struct {
	mu      sync.RWMutex
	byTrace map[string]uuid.UUID
}

func NewTraceOrgIndex() *TraceOrgIndex {
	return &TraceOrgIndex{byTrace: make(map[string]uuid.UUID)}
}

func (t *TraceOrgIndex) RecordEnvelope(envelope *notificationsv1.NotificationEnvelope) {
	if envelope == nil {
		return
	}
	traceID, ok := traceIDFromRooms(envelope.GetRooms())
	if !ok {
		return
	}
	orgID, ok := organizationIDFromPayload(envelope.GetPayload())
	if !ok {
		return
	}
	t.mu.Lock()
	t.byTrace[traceID] = orgID
	t.mu.Unlock()
}

func (t *TraceOrgIndex) OrgIDForTrace(traceID string) (uuid.UUID, bool) {
	t.mu.RLock()
	orgID, ok := t.byTrace[traceID]
	t.mu.RUnlock()
	return orgID, ok
}

func traceIDFromRooms(rooms []string) (string, bool) {
	if len(rooms) == 0 {
		return "", false
	}
	for _, room := range rooms {
		if traceID, matched, err := parseRoomTraceID(room); matched && err == nil {
			return traceID, true
		}
	}
	return "", false
}

func organizationIDFromPayload(payload *structpb.Struct) (uuid.UUID, bool) {
	if payload == nil {
		return uuid.UUID{}, false
	}
	field := payload.GetFields()["organization_id"]
	if field == nil {
		return uuid.UUID{}, false
	}
	stringValue, ok := field.Kind.(*structpb.Value_StringValue)
	if !ok {
		return uuid.UUID{}, false
	}
	orgID, err := parseUUID(stringValue.StringValue)
	if err != nil {
		return uuid.UUID{}, false
	}
	return orgID, true
}
