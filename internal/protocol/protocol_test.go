package protocol

import (
	"bytes"
	"testing"
)

func TestMessageEncodeDecode(t *testing.T) {
	original := Message{
		SID:        "s1",
		Seq:        1,
		Type:       TypeOpen,
		TargetHost: "example.com",
		TargetPort: 443,
	}

	var buf bytes.Buffer
	if err := EncodeMessage(&buf, original); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeMessage(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != original {
		t.Fatalf("decoded mismatch: got %#v want %#v", got, original)
	}
}

func TestDataBase64Validation(t *testing.T) {
	msg := Message{
		SID:  "s1",
		Seq:  2,
		Type: TypeData,
		Data: EncodeBytes([]byte("hello")),
	}
	if err := ValidateMessage(msg); err != nil {
		t.Fatalf("valid data rejected: %v", err)
	}

	msg.Data = "not base64"
	if err := ValidateMessage(msg); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}

func TestDownResponseEncodeDecode(t *testing.T) {
	original := DownResponse{
		SID: "s1",
		Ack: 10,
		Chunks: []Message{
			{SID: "s1", Seq: 1, Type: TypeData, Data: EncodeBytes([]byte("abc"))},
			{SID: "s1", Seq: 2, Type: TypeClose},
		},
	}

	var buf bytes.Buffer
	if err := EncodeDownResponse(&buf, original); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeDownResponse(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SID != original.SID || got.Ack != original.Ack || len(got.Chunks) != len(original.Chunks) {
		t.Fatalf("decoded mismatch: got %#v want %#v", got, original)
	}
}
