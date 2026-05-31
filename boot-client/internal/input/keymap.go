package input

// Linux evdev event types (<linux/input-event-codes.h>).
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03
)

// Relative axes.
const (
	relX = 0x00
	relY = 0x01
)

// Absolute axes (touchpads / VMware/Hyper-V absolute pointers).
const (
	absX = 0x00
	absY = 0x01
)

// A subset of KEY_*/BTN_* codes we care about.
const (
	keyEsc       = 1
	keyBackspace = 14
	keyTab       = 15
	keyEnter     = 28
	keyUp        = 103
	keyLeft      = 105
	keyRight     = 106
	keyDown      = 108
	keyKpEnter   = 96
	btnLeft      = 0x110
	btnTouch     = 0x14a
)

// navKey maps an evdev key code to a logical navigation Key, or KeyNone.
func navKey(code uint16) Key {
	switch int(code) {
	case keyUp:
		return KeyUp
	case keyDown:
		return KeyDown
	case keyLeft:
		return KeyLeft
	case keyRight:
		return KeyRight
	case keyEnter, keyKpEnter:
		return KeyEnter
	case keyEsc:
		return KeyEscape
	case keyBackspace:
		return KeyBackspace
	case keyTab:
		return KeyTab
	}
	return KeyNone
}

// asciiForKey maps an evdev key code to a printable rune, honouring shift
// for the characters PIN/text entry needs (digits + basic punctuation +
// letters). Returns 0 when the key isn't a printable character.
func asciiForKey(code uint16, shift bool) rune {
	// US layout row tables keyed by evdev code.
	base, ok := keyRuneBase[int(code)]
	if !ok {
		return 0
	}
	if shift {
		if r, ok := keyRuneShift[int(code)]; ok {
			return r
		}
		if base >= 'a' && base <= 'z' {
			return base - 32
		}
	}
	return base
}

var keyRuneBase = map[int]rune{
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	12: '-', 13: '=',
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	26: '[', 27: ']',
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	39: ';', 40: '\'', 41: '`',
	43: '\\',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm',
	51: ',', 52: '.', 53: '/',
	57: ' ',
	// keypad digits
	71: '7', 72: '8', 73: '9', 75: '4', 76: '5', 77: '6', 79: '1', 80: '2', 81: '3', 82: '0',
}

var keyRuneShift = map[int]rune{
	2: '!', 3: '@', 4: '#', 5: '$', 6: '%', 7: '^', 8: '&', 9: '*', 10: '(', 11: ')',
	12: '_', 13: '+',
	26: '{', 27: '}', 39: ':', 40: '"', 41: '~', 43: '|',
	51: '<', 52: '>', 53: '?',
}

const (
	keyLeftShift  = 42
	keyRightShift = 54
)
