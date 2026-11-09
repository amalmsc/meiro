// Package romanize gives lyric lines a Latin-letter reading. Kana and Hangul
// convert directly. Kanji and hanzi need a dictionary this package does not
// carry, so a line containing either is left for the lyrics source.
package romanize

import "strings"

// Line returns text in Latin letters, or empty when it cannot.
func Line(text string) string {
	var out strings.Builder
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			return ""
		case r >= 0xAC00 && r <= 0xD7A3:
			out.WriteString(hangul(r))
		case isKana(r):
			out.WriteString(kana(r))
		default:
			out.WriteRune(r)
		}
	}
	if !changed(text, out.String()) {
		return ""
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func changed(original, converted string) bool {
	return strings.TrimSpace(converted) != "" && strings.TrimSpace(converted) != strings.TrimSpace(original)
}

func isKana(r rune) bool { return r >= 0x3041 && r <= 0x30FF || r == 'ー' }

var kanaMap = map[rune]string{
	'あ': "a", 'い': "i", 'う': "u", 'え': "e", 'お': "o",
	'か': "ka", 'き': "ki", 'く': "ku", 'け': "ke", 'こ': "ko",
	'さ': "sa", 'し': "shi", 'す': "su", 'せ': "se", 'そ': "so",
	'た': "ta", 'ち': "chi", 'つ': "tsu", 'て': "te", 'と': "to",
	'な': "na", 'に': "ni", 'ぬ': "nu", 'ね': "ne", 'の': "no",
	'は': "ha", 'ひ': "hi", 'ふ': "fu", 'へ': "he", 'ほ': "ho",
	'ま': "ma", 'み': "mi", 'む': "mu", 'め': "me", 'も': "mo",
	'や': "ya", 'ゆ': "yu", 'よ': "yo", 'ら': "ra", 'り': "ri", 'る': "ru", 'れ': "re", 'ろ': "ro",
	'わ': "wa", 'を': "o", 'ん': "n",
	'が': "ga", 'ぎ': "gi", 'ぐ': "gu", 'げ': "ge", 'ご': "go",
	'ざ': "za", 'じ': "ji", 'ず': "zu", 'ぜ': "ze", 'ぞ': "zo",
	'だ': "da", 'ぢ': "ji", 'づ': "zu", 'で': "de", 'ど': "do",
	'ば': "ba", 'び': "bi", 'ぶ': "bu", 'べ': "be", 'ぼ': "bo",
	'ぱ': "pa", 'ぴ': "pi", 'ぷ': "pu", 'ぺ': "pe", 'ぽ': "po",
	'ぁ': "a", 'ぃ': "i", 'ぅ': "u", 'ぇ': "e", 'ぉ': "o",
	'ゃ': "ya", 'ゅ': "yu", 'ょ': "yo", 'っ': "",
}

func init() {
	for r, reading := range map[rune]string{} {
		_ = r
		_ = reading
	}
	extra := map[rune]string{}
	for r, reading := range kanaMap {
		extra[r+0x60] = reading
	}
	for r, reading := range extra {
		kanaMap[r] = reading
	}
	kanaMap['ヴ'] = "vu"
}

func kana(r rune) string { return kanaMap[r] }

var lead = []string{"g", "kk", "n", "d", "tt", "r", "m", "b", "pp", "s", "ss", "", "j", "jj", "ch", "k", "t", "p", "h"}
var vowel = []string{"a", "ae", "ya", "yae", "eo", "e", "yeo", "ye", "o", "wa", "wae", "oe", "yo", "u", "wo", "we", "wi", "yu", "eu", "ui", "i"}
var tail = []string{"", "k", "k", "ks", "n", "nj", "nh", "t", "l", "lk", "lm", "lb", "ls", "lt", "lp", "lh", "m", "p", "ps", "t", "t", "ng", "t", "t", "k", "t", "p", "t"}

func hangul(r rune) string {
	n := int(r - 0xAC00)
	return lead[n/588] + vowel[(n%588)/28] + tail[n%28]
}
