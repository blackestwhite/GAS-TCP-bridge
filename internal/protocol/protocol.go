package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	TypeOpen  = "open"
	TypeData  = "data"
	TypeClose = "close"
	TypeError = "error"
)

type Message struct {
	SID        string `json:"sid"`
	Seq        uint64 `json:"seq"`
	Type       string `json:"type"`
	TargetHost string `json:"target_host,omitempty"`
	TargetPort int    `json:"target_port,omitempty"`
	Data       string `json:"data,omitempty"`
	Message    string `json:"message,omitempty"`
}

type DownResponse struct {
	SID    string    `json:"sid"`
	Ack    uint64    `json:"ack"`
	Chunks []Message `json:"chunks"`
}

type AckResponse struct {
	SID   string `json:"sid"`
	Ack   uint64 `json:"ack"`
	Error string `json:"error,omitempty"`
}

func EncodeMessage(w io.Writer, msg Message) error {
	return json.NewEncoder(w).Encode(msg)
}

func DecodeMessage(r io.Reader) (Message, error) {
	var msg Message
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func EncodeDownResponse(w io.Writer, resp DownResponse) error {
	return json.NewEncoder(w).Encode(resp)
}

func DecodeDownResponse(r io.Reader) (DownResponse, error) {
	var resp DownResponse
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return DownResponse{}, err
	}
	return resp, nil
}

func EncodeAckResponse(w io.Writer, resp AckResponse) error {
	return json.NewEncoder(w).Encode(resp)
}

func DecodeAckResponse(r io.Reader) (AckResponse, error) {
	var resp AckResponse
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return AckResponse{}, err
	}
	return resp, nil
}

func EncodeBytes(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func DecodeBytes(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func ValidateMessage(msg Message) error {
	if msg.SID == "" {
		return errors.New("sid is required")
	}
	if msg.Seq == 0 {
		return errors.New("seq must be greater than zero")
	}
	switch msg.Type {
	case TypeOpen:
		if msg.TargetPort < 0 || msg.TargetPort > 65535 {
			return fmt.Errorf("invalid target port %d", msg.TargetPort)
		}
	case TypeData:
		if msg.Data == "" {
			return errors.New("data is required")
		}
		if _, err := DecodeBytes(msg.Data); err != nil {
			return fmt.Errorf("invalid base64 data: %w", err)
		}
	case TypeClose:
	case TypeError:
		if msg.Message == "" {
			return errors.New("error message is required")
		}
	default:
		return fmt.Errorf("unknown message type %q", msg.Type)
	}
	return nil
}

func ApproxPayloadSize(msg Message) int {
	if msg.Type == TypeData && msg.Data != "" {
		return base64.StdEncoding.DecodedLen(len(msg.Data))
	}
	if msg.Type == TypeOpen || msg.Type == TypeClose || msg.Type == TypeError {
		return 1
	}
	return 0
}
