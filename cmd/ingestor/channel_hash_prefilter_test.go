package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func testChannelKeyHash(keyHex string) byte {
	key, _ := hex.DecodeString(keyHex)
	h := sha256.Sum256(key)
	return h[0]
}

func TestChannelKeyMatchesHash(t *testing.T) {
	const public = "8b3387e9c5cdea6ac9e5edbaa115cd72"
	if !channelKeyMatchesHash(public, 0x11) || !channelKeyMatchesHash(strings.ToUpper(public), 0x11) {
		t.Fatal("firmware public channel hash must match 0x11")
	}
	for _, bad := range []string{"", "0011", strings.Repeat("00", 17), strings.Repeat("GG", 16)} {
		if channelKeyMatchesHash(bad, 0x11) {
			t.Fatalf("invalid key accepted: %q", bad)
		}
	}
	if channelKeyMatchesHash(public, 0x12) {
		t.Fatal("mismatched hash accepted")
	}
}

func TestGroupHashCollisionStillRequiresMAC(t *testing.T) {
	correct := deriveHashtagChannelKey("#prefilter-collision-test")
	want := testChannelKeyHash(correct)
	colliding := ""
	for i := 0; i < 10000; i++ {
		k := deriveHashtagChannelKey(fmt.Sprintf("#collision-%d", i))
		if testChannelKeyHash(k) == want && k != correct {
			colliding = k
			break
		}
	}
	if colliding == "" {
		t.Fatal("no collision found")
	}
	txt, mac := buildTestCiphertext(correct, "Tester: hello", 1700000000)
	ct, _ := hex.DecodeString(txt)
	m, _ := hex.DecodeString(mac)
	buf := append(append([]byte{want}, m...), ct...)
	for i := 0; i < 100; i++ {
		p := decodeGrpTxt(buf, map[string]string{"#wrong": colliding, "#right": correct})
		if p.Channel != "#right" || p.Sender != "Tester" {
			t.Fatalf("hash match bypassed MAC: %+v", p)
		}
	}
	if p := decodeGrpTxt(buf, map[string]string{"#wrong": colliding}); p.DecryptionStatus != "decryption_failed" {
		t.Fatal("collision decrypted without correct key")
	}
	// A valid MAC does not override a different envelope channel hash.
	buf[0]++
	if p := decodeGrpTxt(buf, map[string]string{"#right": correct}); p.DecryptionStatus != "decryption_failed" {
		t.Fatal("wrong channel hash decrypted")
	}
}

func TestGroupDataHashCollisionStillRequiresMAC(t *testing.T) {
	key := deriveHashtagChannelKey("#binary-test")
	want := testChannelKeyHash(key)
	wrong := ""
	for i := 0; i < 10000; i++ {
		k := deriveHashtagChannelKey(fmt.Sprint("#binary-collision", i))
		if testChannelKeyHash(k) == want && k != key {
			wrong = k
			break
		}
	}
	if wrong == "" {
		t.Fatal("no collision")
	}
	ctHex, macHex := buildChannelEncrypted(key, []byte{1, 0, 2, 0xaa, 0xbb})
	ct, _ := hex.DecodeString(ctHex)
	mac, _ := hex.DecodeString(macHex)
	buf := append(append([]byte{want}, mac...), ct...)
	p := decodeGrpData(buf, map[string]string{"#wrong": wrong, "#binary-test": key})
	if p.Channel != "#binary-test" || p.DecryptionStatus != "decrypted" {
		t.Fatalf("unexpected result: %+v", p)
	}
	buf[0]++
	if p := decodeGrpData(buf, map[string]string{"#binary-test": key}); p.DecryptionStatus != "decryption_failed" {
		t.Fatal("wrong hash decrypted")
	}
}

func BenchmarkGroupChannelCatalogue(b *testing.B) {
	for _, n := range []int{320, 2703} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			keys := map[string]string{}
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("#catalogue-%d", i)
				keys[name] = deriveHashtagChannelKey(name)
			}
			buf := make([]byte, 131)
			for i := range buf {
				buf[i] = byte(i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				decodeGrpTxt(buf, keys)
			}
		})
	}
}
