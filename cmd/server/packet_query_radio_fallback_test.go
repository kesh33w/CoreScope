package main

import "testing"

func TestRadioQueryNeverFallsBackToUnfilteredSQL(t *testing.T) {
	s := &PacketStore{retentionHours: 2}
	for _, q := range []PacketQuery{
		{Radio: "radio-909", Since: "2000-01-01T00:00:00Z"},
		{Radio: "radio-910", Until: "2000-01-01T00:00:00Z"},
	} {
		unfiltered := q
		unfiltered.Radio = ""
		if !s.packetQueryNeedsSQL(unfiltered) {
			t.Fatal("test query must require historical SQL without radio filter")
		}
		if s.packetQueryNeedsSQL(q) {
			t.Fatal("radio-filtered query would use unfiltered SQL")
		}
	}
}
