package romanize

import "testing"

func TestLineReadsKanaAndHangul(t *testing.T) {
	if got := Line("こんにちは"); got != "konnichiha" {
		t.Fatalf("kana: %q", got)
	}
	if got := Line("안녕"); got != "annyeong" {
		t.Fatalf("hangul: %q", got)
	}
	if got := Line("夢ならば"); got != "" {
		t.Fatalf("kanji should wait for a dictionary reading: %q", got)
	}
}
