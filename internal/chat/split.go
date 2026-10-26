package chat

import (
	"strings"
	"unicode/utf8"
)

const (
	// defaultSegmentMaxChars 是未配置分段长度时的兜底值。
	// 配置层也会补这个默认值，这里再兜一次，保证 Split 单独被调用时不会无限长。
	defaultSegmentMaxChars = 1500
	defaultMaxSegments     = 3

	// truncateSuffix 只在最后一段确实放不下时追加，提示用户内容有裁剪。
	truncateSuffix = "…（内容过长已截断）"
)

// Split 把模型回复切成平台可发送的若干段。
//
// 三级降级（顺序不能换）：
//  1. 不超长 → 原样一段；
//  2. 按 splitOn 切块并贪心装段；单块自身超长时先按 rune 硬切；
//  3. 段数超过 maxSegments → 尾部合并进最后一段；最后一段仍超长 → 按 rune 截断并加后缀。
//
// 所有长度都以 rune 计，不能用 len：中文与 emoji 的字节数不等于字符数。
// 硬切可能拆开 ZWJ 之类组合 emoji 的 grapheme cluster，但结果仍是合法 UTF-8，
// V1 接受这一取舍（不引入第三方分段库）。
func Split(text string, maxChars, maxSegments int, splitOn string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChars <= 0 {
		maxChars = defaultSegmentMaxChars
	}
	if maxSegments <= 0 {
		maxSegments = defaultMaxSegments
	}
	if splitOn == "" {
		splitOn = "\n\n"
	}
	if utf8.RuneCountInString(text) <= maxChars {
		return []string{text}
	}

	// ① 先按自然段切块；空白块直接丢弃（连续分隔符不会产生空段）。
	blocks := strings.Split(text, splitOn)
	units := make([]unit, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		// 单个块本身超长时先硬切，保证后面贪心装段时每个 unit 都装得下。
		// 同一块内部硬切出来的片段用空分隔符直接拼接，不凭空插入空行。
		for i, chunk := range hardSplit(block, maxChars) {
			sep := ""
			if i == 0 && len(units) > 0 {
				sep = splitOn
			}
			units = append(units, unit{text: chunk, sep: sep})
		}
	}
	if len(units) == 0 {
		return nil
	}

	// ② 贪心装段。段首不保留分隔符，避免每条消息都以空行开头。
	segments := make([]string, 0, maxSegments)
	var b strings.Builder
	curLen := 0
	flush := func() {
		if curLen == 0 {
			return
		}
		segments = append(segments, b.String())
		b.Reset()
		curLen = 0
	}
	for _, u := range units {
		sep := u.sep
		if curLen == 0 {
			sep = ""
		}
		add := utf8.RuneCountInString(sep) + utf8.RuneCountInString(u.text)
		if curLen > 0 && curLen+add > maxChars {
			flush()
			sep = ""
			add = utf8.RuneCountInString(u.text)
		}
		b.WriteString(sep)
		b.WriteString(u.text)
		curLen += add
	}
	flush()

	// ③ 段数超限：保留前 maxSegments-1 段，其余全部并进最后一段。
	// 合并后必然更容易超长，所以紧跟着做一次截断。
	if len(segments) > maxSegments {
		head := segments[:maxSegments-1]
		tail := strings.Join(segments[maxSegments-1:], splitOn)
		segments = append(head, tail)
	}
	last := len(segments) - 1
	if utf8.RuneCountInString(segments[last]) > maxChars {
		segments[last] = truncateWithSuffix(segments[last], maxChars)
	}
	return segments
}

// unit 是装段的最小单位：text 本身，加上它前面的分隔符（段首会被忽略）。
type unit struct {
	text string
	sep  string
}

// hardSplit 按 rune 把超长块切成每段不超过 maxChars 的片段。
func hardSplit(s string, maxChars int) []string {
	r := []rune(s)
	if len(r) <= maxChars {
		return []string{s}
	}
	out := make([]string, 0, (len(r)+maxChars-1)/maxChars)
	for len(r) > 0 {
		n := min(maxChars, len(r))
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}

// truncateWithSuffix 把 s 截到 maxChars 个 rune，并在末尾加截断后缀。
// maxChars 连后缀都放不下时宁可不加后缀，也不能让结果超过上限。
func truncateWithSuffix(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	suffix := []rune(truncateSuffix)
	if maxChars <= len(suffix) {
		return string(r[:maxChars])
	}
	return string(r[:maxChars-len(suffix)]) + truncateSuffix
}
