package onebot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"multiparse/internal/model"
)

func validAccountID(id string) bool {
	if len(id) == 0 || len(id) > 19 || id[0] < '1' || id[0] > '9' {
		return false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0
}

func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rawInteger(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	value := string(raw)
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
		value = text
	}
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "+") {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil
}

func rawAccount(raw json.RawMessage) (int64, bool) {
	n, ok := rawInteger(raw)
	return n, ok && n > 0
}

func (c *connection) readLoop() {
	defer c.shutdown(errors.New("OneBot connection closed"))
	for {
		kind, payload, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		c.lastSeen.Store(time.Now().UnixNano())
		if kind != websocket.TextMessage {
			c.shutdown(errors.New("OneBot requires JSON text frames"))
			return
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope == nil {
			c.shutdown(errors.New("invalid OneBot JSON frame"))
			return
		}
		postType := rawString(envelope["post_type"])
		if postType == "" {
			c.handleResponse(envelope)
			continue
		}
		if postType != "message" {
			continue
		}
		event, ok := decodeEvent(c.selfID, envelope)
		if !ok {
			continue
		}
		if !c.hub.queueEvent(c, event, time.Now()) {
			// Disconnect rather than let a slow event handler block response demux
			// or silently accept an unbounded number of messages.
			c.shutdown(errors.New("OneBot event queue is full"))
			return
		}
	}
}

func (c *connection) handleResponse(envelope map[string]json.RawMessage) {
	echo := rawString(envelope["echo"])
	if echo == "" || len(echo) > 128 {
		return
	}
	c.mu.Lock()
	request := c.pending[echo]
	c.mu.Unlock()
	if request == nil {
		return
	}
	status := rawString(envelope["status"])
	retcode, hasCode := rawInteger(envelope["retcode"])
	if !hasCode || status == "" {
		c.finish(request, errors.New("invalid OneBot acknowledgement"), true)
		return
	}
	if retcode != 0 || status != "ok" {
		message := rawString(envelope["wording"])
		if message == "" {
			message = rawString(envelope["message"])
		}
		if message == "" {
			message = rawString(envelope["msg"])
		}
		if len(message) > 4096 {
			message = message[:4096]
		}
		apiErr := &APIError{Action: request.action, Status: status, RetCode: retcode, Message: message}
		c.finish(request, apiErr, status == "async" || retcode == 1)
		return
	}
	c.finish(request, nil, false)
}

func decodeEvent(selfID string, envelope map[string]json.RawMessage) (model.Event, bool) {
	var event model.Event
	if rawString(envelope["post_type"]) != "message" {
		return event, false
	}
	if raw, exists := envelope["self_id"]; exists {
		id, ok := rawAccount(raw)
		if !ok || strconv.FormatInt(id, 10) != selfID {
			return event, false
		}
	}
	userID, ok := rawAccount(envelope["user_id"])
	if !ok || strconv.FormatInt(userID, 10) == selfID {
		return event, false
	}
	var sender map[string]json.RawMessage
	if json.Unmarshal(envelope["sender"], &sender) == nil {
		if id, ok := rawAccount(sender["user_id"]); ok && strconv.FormatInt(id, 10) == selfID {
			return event, false
		}
	}
	event.Target = model.Target{BotID: selfID, UserID: userID}
	switch rawString(envelope["message_type"]) {
	case "group":
		groupID, ok := rawAccount(envelope["group_id"])
		if !ok {
			return model.Event{}, false
		}
		event.GroupID = groupID
	case "private":
	default:
		return model.Event{}, false
	}
	messageID := bytes.TrimSpace(envelope["message_id"])
	if len(messageID) == 0 {
		return model.Event{}, false
	}
	if messageID[0] == '"' {
		event.MessageID = rawString(messageID)
	} else if id, ok := rawInteger(messageID); ok {
		event.MessageID = strconv.FormatInt(id, 10)
	}
	if event.MessageID == "" || len(event.MessageID) > 128 {
		return model.Event{}, false
	}
	message := bytes.TrimSpace(envelope["message"])
	if len(message) == 0 {
		return model.Event{}, false
	}
	if message[0] == '"' {
		var text string
		if json.Unmarshal(message, &text) != nil {
			return model.Event{}, false
		}
		event.Segments = []model.Segment{{Type: "text", Data: map[string]any{"text": text}}}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(message))
		decoder.UseNumber()
		if decoder.Decode(&event.Segments) != nil || len(event.Segments) == 0 || len(event.Segments) > 200 {
			return model.Event{}, false
		}
	}
	return event, true
}

func (h *Hub) queueEvent(c *connection, event model.Event, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.connections[c.selfID] != c || !c.started || !h.authorizedLocked(c) {
		return true
	}
	// Messages produced by another managed account must not form bot-to-bot loops.
	senderID := strconv.FormatInt(event.UserID, 10)
	if _, own := h.connections[senderID]; own {
		return true
	}
	for _, own := range h.auth.SelfIDs {
		if senderID == own {
			return true
		}
	}
	for h.dedupHead < len(h.dedupOrder) && !h.dedupOrder[h.dedupHead].expires.After(now) {
		h.evictDedupLocked()
	}
	key := fmt.Sprintf("%s/%d/%d/%s", event.BotID, event.GroupID, event.UserID, event.MessageID)
	if expiry, exists := h.dedup[key]; exists && expiry.After(now) {
		return true
	}
	select {
	case c.events <- event:
	default:
		return false
	}
	for len(h.dedup) >= maxDedupEntries {
		h.evictDedupLocked()
	}
	expires := now.Add(dedupTTL)
	h.dedup[key] = expires
	h.dedupOrder = append(h.dedupOrder, dedupEntry{key: key, expires: expires})
	if h.dedupHead >= 1024 && h.dedupHead*2 >= len(h.dedupOrder) {
		copy(h.dedupOrder, h.dedupOrder[h.dedupHead:])
		h.dedupOrder = h.dedupOrder[:len(h.dedupOrder)-h.dedupHead]
		h.dedupHead = 0
	}
	return true
}

func (h *Hub) evictDedupLocked() {
	entry := h.dedupOrder[h.dedupHead]
	if expiry, exists := h.dedup[entry.key]; exists && expiry.Equal(entry.expires) {
		delete(h.dedup, entry.key)
	}
	h.dedupOrder[h.dedupHead] = dedupEntry{}
	h.dedupHead++
}
