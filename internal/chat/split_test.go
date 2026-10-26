package chat

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func assertSegmentLimit(t *testing.T, segments []string, maxChars int) {
	t.Helper()
	for i, s := range segments {
		if n := utf8.RuneCountInString(s); n > maxChars {
			t.Fatalf("第 %d 段长度 = %d rune，超过上限 %d: %q", i, n, maxChars, s)
		}
	}
}

func TestSplit_ShortTextUnchanged(t *testing.T) {
	got := Split("  你好，世界  ", 100, 3, "\n\n")
	if len(got) != 1 || got[0] != "你好，世界" {
		t.Fatalf("Split = %#v，期望原样一段（仅去首尾空白）", got)
	}
}

func TestSplit_EmptyText(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\n", "\t"} {
		if got := Split(text, 100, 3, "\n\n"); got != nil {
			t.Fatalf("Split(%q) = %#v，期望 nil", text, got)
		}
	}
}

func TestSplit_ParagraphsNotSplitWhenFits(t *testing.T) {
	text := "第一段\n\n第二段"
	got := Split(text, 100, 3, "\n\n")
	if len(got) != 1 || got[0] != text {
		t.Fatalf("Split = %#v，期望不拆段", got)
	}
}

func TestSplit_ChineseRunesNotBytes(t *testing.T) {
	text := strings.Repeat("长", 250)
	got := Split(text, 100, 3, "\n\n")

	if len(got) != 3 {
		t.Fatalf("段数 = %d，期望 3", len(got))
	}
	assertSegmentLimit(t, got, 100)
	if strings.Join(got, "") != text {
		t.Fatal("未发生截断时，拼接应等于原文")
	}
}

func TestSplit_MergesOverflowIntoLastSegment(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("a\n\n", 600))
	got := Split(text, 100, 3, "\n\n")

	if len(got) != 3 {
		t.Fatalf("段数 = %d，期望 3（超出的段应合并进最后一段）", len(got))
	}
	assertSegmentLimit(t, got, 100)
	if !strings.HasSuffix(got[2], truncateSuffix) {
		t.Fatalf("最后一段缺少截断后缀: %q", got[2])
	}
}

func TestSplit_TruncatesLastSegmentWithSuffix(t *testing.T) {
	text := strings.Repeat("长", 5000)
	got := Split(text, 100, 3, "\n\n")

	if len(got) != 3 {
		t.Fatalf("段数 = %d，期望 3", len(got))
	}
	assertSegmentLimit(t, got, 100)
	if !strings.HasSuffix(got[2], truncateSuffix) {
		t.Fatalf("最后一段缺少截断后缀: %q", got[2])
	}
	// 截断只发生在末尾，前两段 + 第三段的前缀必须与原文一致。
	prefix := strings.Join(got[:2], "")
	if !strings.HasPrefix(text, prefix) {
		t.Fatal("前两段不是原文前缀")
	}
}

func TestSplit_EmojiStaysValidUTF8(t *testing.T) {
	text := strings.Repeat("👨‍👩‍👧‍👦", 100)
	got := Split(text, 10, 3, "\n\n")

	assertSegmentLimit(t, got, 10)
	for i, s := range got {
		if !utf8.ValidString(s) {
			t.Fatalf("第 %d 段不是合法 UTF-8: %q", i, s)
		}
	}
}

func TestSplit_Defaults(t *testing.T) {
	text := strings.Repeat("长", 2000)
	got := Split(text, 0, 0, "")

	if len(got) != 2 {
		t.Fatalf("段数 = %d，期望 2（默认上限 1500/段）", len(got))
	}
	assertSegmentLimit(t, got, defaultSegmentMaxChars)
}

func TestSplit_SingleSegmentTruncates(t *testing.T) {
	text := strings.Repeat("长", 500)
	got := Split(text, 100, 1, "\n\n")

	if len(got) != 1 {
		t.Fatalf("段数 = %d，期望 1", len(got))
	}
	assertSegmentLimit(t, got, 100)
	if !strings.HasSuffix(got[0], truncateSuffix) {
		t.Fatalf("唯一一段缺少截断后缀: %q", got[0])
	}
}
