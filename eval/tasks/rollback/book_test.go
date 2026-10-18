package task

import (
	"reflect"
	"testing"
)

func TestReserveIsAllOrNothing(t *testing.T) {
	book := NewBook()
	if err := book.Reserve("A"); err != nil {
		t.Fatalf("Reserve(A) = %v", err)
	}
	if err := book.Reserve("B", "C"); err != nil {
		t.Fatalf("Reserve(B, C) = %v", err)
	}
	if err := book.Reserve("D", "A"); err == nil {
		t.Fatal("reserving a taken seat succeeded")
	}
	want := []string{"A", "B", "C"}
	if got := book.Reserved(); !reflect.DeepEqual(got, want) {
		t.Errorf("after a refused reservation the book holds %v, want %v", got, want)
	}
}

func TestReleaseIsAllOrNothing(t *testing.T) {
	book := NewBook()
	if err := book.Reserve("A", "B"); err != nil {
		t.Fatalf("Reserve(A, B) = %v", err)
	}
	if err := book.Release("A", "Z"); err == nil {
		t.Fatal("releasing a seat that is not taken succeeded")
	}
	want := []string{"A", "B"}
	if got := book.Reserved(); !reflect.DeepEqual(got, want) {
		t.Errorf("after a refused release the book holds %v, want %v", got, want)
	}
	if err := book.Release("A", "B"); err != nil {
		t.Fatalf("Release(A, B) = %v", err)
	}
	if got := book.Reserved(); len(got) != 0 {
		t.Errorf("the book still holds %v", got)
	}
}
