package multibase

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

var base256emojiTable = [256]rune{
	// Curated list, this is just a list of things that *somwhat* are related to our comunity
	'🚀', '🪐', '☄', '🛰', '🌌', // Space
	'🌑', '🌒', '🌓', '🌔', '🌕', '🌖', '🌗', '🌘', // Moon
	'🌍', '🌏', '🌎', // Our Home, for now (earth)
	'🐉',                // Dragon!!!
	'☀',                // Our Garden, for now (sol)
	'💻', '🖥', '💾', '💿', // Computer
	// The rest is completed from https://home.unicode.org/emoji/emoji-frequency/ at the time of creation (december 2021) (the data is from 2019), most used first until we reach 256.
	// We exclude modifier based emojies (such as flags) as they are bigger than one single codepoint.
	// Some other emojies were removed adhoc for various reasons.
	'😂', '❤', '😍', '🤣', '😊', '🙏', '💕', '😭', '😘', '👍',
	'😅', '👏', '😁', '🔥', '🥰', '💔', '💖', '💙', '😢', '🤔',
	'😆', '🙄', '💪', '😉', '☺', '👌', '🤗', '💜', '😔', '😎',
	'😇', '🌹', '🤦', '🎉', '💞', '✌', '✨', '🤷', '😱', '😌',
	'🌸', '🙌', '😋', '💗', '💚', '😏', '💛', '🙂', '💓', '🤩',
	'😄', '😀', '🖤', '😃', '💯', '🙈', '👇', '🎶', '😒', '🤭',
	'❣', '😜', '💋', '👀', '😪', '😑', '💥', '🙋', '😞', '😩',
	'😡', '🤪', '👊', '🥳', '😥', '🤤', '👉', '💃', '😳', '✋',
	'😚', '😝', '😴', '🌟', '😬', '🙃', '🍀', '🌷', '😻', '😓',
	'⭐', '✅', '🥺', '🌈', '😈', '🤘', '💦', '✔', '😣', '🏃',
	'💐', '☹', '🎊', '💘', '😠', '☝', '😕', '🌺', '🎂', '🌻',
	'😐', '🖕', '💝', '🙊', '😹', '🗣', '💫', '💀', '👑', '🎵',
	'🤞', '😛', '🔴', '😤', '🌼', '😫', '⚽', '🤙', '☕', '🏆',
	'🤫', '👈', '😮', '🙆', '🍻', '🍃', '🐶', '💁', '😲', '🌿',
	'🧡', '🎁', '⚡', '🌞', '🎈', '❌', '✊', '👋', '😰', '🤨',
	'😶', '🤝', '🚶', '💰', '🍓', '💢', '🤟', '🙁', '🚨', '💨',
	'🤬', '✈', '🎀', '🍺', '🤓', '😙', '💟', '🌱', '😖', '👶',
	'🥴', '▶', '➡', '❓', '💎', '💸', '⬇', '😨', '🌚', '🦋',
	'😷', '🕺', '⚠', '🙅', '😟', '😵', '👎', '🤲', '🤠', '🤧',
	'📌', '🔵', '💅', '🧐', '🐾', '🍒', '😗', '🤑', '🌊', '🤯',
	'🐷', '☎', '💧', '😯', '💆', '👆', '🎤', '🙇', '🍑', '❄',
	'🌴', '💣', '🐸', '💌', '📍', '🥀', '🤢', '👅', '💡', '💩',
	'👐', '📸', '👻', '🤐', '🤮', '🎼', '🥵', '🚩', '🍎', '🍊',
	'👼', '💍', '📣', '🥂',
}

var base256emojiReverseTable map[rune]byte

func init() {
	base256emojiReverseTable = make(map[rune]byte, len(base256emojiTable))
	for i, v := range base256emojiTable {
		base256emojiReverseTable[v] = byte(i)
	}
}

func base256emojiEncode(in []byte) string {
	var l int
	for _, v := range in {
		l += utf8.RuneLen(base256emojiTable[v])
	}
	var out strings.Builder
	out.Grow(l)
	for _, v := range in {
		out.WriteRune(base256emojiTable[v])
	}
	return out.String()
}

type base256emojiCorruptInputError struct {
	index int
	char  rune
}

func (e base256emojiCorruptInputError) Error() string {
	return "illegal base256emoji data at input byte " + strconv.FormatInt(int64(e.index), 10) + ", char: '" + string(e.char) + "'"
}

func (e base256emojiCorruptInputError) String() string {
	return e.Error()
}

func base256emojiDecode(in string) ([]byte, error) {
	out := make([]byte, utf8.RuneCountInString(in))
	var stri int
	for i := 0; len(in) > 0; i++ {
		r, n := utf8.DecodeRuneInString(in)
		in = in[n:]
		var ok bool
		out[i], ok = base256emojiReverseTable[r]
		if !ok {
			return nil, base256emojiCorruptInputError{stri, r}
		}
		stri += n
	}
	return out, nil
}
