package core

import (
	"testing"
	"time"
)

func TestTerminationRequestRequiresConfirmation(t *testing.T) {
	request := NewTerminationRequest("session-1", []Process{{PID: 10, CreatedAt: time.Unix(100, 0)}})
	if request.Confirmed() {
		t.Fatal("new termination request was confirmed")
	}
	if !request.Confirm().Confirmed() {
		t.Fatal("confirmed termination request was not confirmed")
	}
}

func TestRevalidateObservedRejectsChangedProcessTree(t *testing.T) {
	created := time.Unix(100, 0)
	request := NewTerminationRequest("observed-1", []Process{
		{PID: 10, CreatedAt: created},
		{PID: 11, CreatedAt: created.Add(time.Second)},
	}).Confirm()

	current := []Process{
		{PID: 10, CreatedAt: created},
		{PID: 11, CreatedAt: created.Add(2 * time.Second)},
	}
	if err := RevalidateObserved(request, current); err == nil {
		t.Fatal("RevalidateObserved() accepted a reused child PID")
	}
}

func TestRevalidateObservedAcceptsUnchangedTree(t *testing.T) {
	created := time.Unix(100, 0)
	current := []Process{{PID: 10, CreatedAt: created}, {PID: 11, CreatedAt: created.Add(time.Second)}}
	request := NewTerminationRequest("observed-1", current).Confirm()

	if err := RevalidateObserved(request, current); err != nil {
		t.Fatalf("RevalidateObserved() error = %v", err)
	}
}
