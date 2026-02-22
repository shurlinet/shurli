package p2pnet

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

// emojiTable contains 256 universally recognizable emoji for SAS encoding.
// 8 bytes of hash = 4 emoji (2 bytes per emoji index, mod 256).
var emojiTable = [256]string{
	// Animals
	"🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼",
	"🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🐔",
	"🐧", "🐦", "🐤", "🦆", "🦅", "🦉", "🦇", "🐺",
	"🐗", "🐴", "🦄", "🐝", "🐛", "🦋", "🐌", "🐞",
	// Sea creatures
	"🐙", "🦑", "🦐", "🦀", "🐡", "🐠", "🐟", "🐬",
	"🐳", "🐋", "🦈", "🐊", "🐅", "🐆", "🦓", "🦍",
	// More animals
	"🐘", "🦛", "🦏", "🐪", "🐫", "🦒", "🦘", "🐃",
	"🐂", "🐄", "🐎", "🐖", "🐏", "🐑", "🦙", "🐐",
	// Nature
	"🌵", "🎄", "🌲", "🌳", "🌴", "🌱", "🌿", "🍀",
	"🍁", "🍂", "🍃", "🌺", "🌻", "🌹", "🥀", "🌷",
	"🌼", "🌸", "💐", "🍄", "🌰", "🎃", "🌑", "🌒",
	"🌓", "🌔", "🌕", "🌖", "🌗", "🌘", "🌙", "🌚",
	// Weather
	"⭐", "🌟", "💫", "✨", "☄️", "🌤️", "⛅", "🌥️",
	"🌦️", "🌧️", "⛈️", "🌩️", "🌪️", "🌈", "☀️", "🌊",
	// Food
	"🍎", "🍊", "🍋", "🍌", "🍉", "🍇", "🍓", "🍈",
	"🍒", "🍑", "🥭", "🍍", "🥥", "🥝", "🍅", "🥑",
	"🌶️", "🥕", "🥔", "🧅", "🌽", "🥦", "🥒", "🥬",
	"🍆", "🥜", "🫘", "🍞", "🥐", "🥖", "🧀", "🥚",
	// Objects
	"🔑", "🗝️", "🔒", "🔓", "🔨", "🪓", "⛏️", "🔧",
	"🔩", "⚙️", "🧲", "🔫", "💣", "🧨", "🪚", "🔪",
	"🗡️", "🛡️", "🏹", "🎯", "🪃", "🧰", "🔬", "🔭",
	"📡", "💉", "🩸", "💊", "🩹", "🧬", "🦠", "🧫",
	// Musical
	"🎸", "🎹", "🥁", "🎺", "🎷", "🪗", "🎻", "🪕",
	"🎵", "🎶", "🎼", "🎤", "🎧", "📻", "🎙️", "📯",
	// Transport
	"🚀", "🛸", "🚁", "⛵", "🚂", "🚗", "🚕", "🏎️",
	"🚌", "🚎", "🚑", "🚒", "🛻", "🚜", "🛵", "🏍️",
	// Sports/Games
	"⚽", "🏀", "🏈", "⚾", "🥎", "🎾", "🏐", "🏉",
	"🎱", "🏓", "🏸", "🥊", "🎿", "⛷️", "🏂", "🪂",
	// Symbols
	"❤️", "🧡", "💛", "💚", "💙", "💜", "🤎", "🖤",
	"💎", "🔥", "💧", "🌀", "🎪", "🎭", "🎨", "🧩",
	"♟️", "🎲", "🧸", "🪆", "🪄", "🎩", "👑", "💍",
	"🏆", "🥇", "🥈", "🥉", "🏅", "🎖️", "🏵️", "🎗️",
}

// ComputeFingerprint computes a deterministic SAS fingerprint for a peer pair.
// Both peers compute the same fingerprint because both know both peer IDs.
// Returns both emoji and numeric representations.
func ComputeFingerprint(a, b peer.ID) (emoji string, numeric string) {
	// Sort peer IDs for deterministic order.
	aBytes := []byte(a)
	bBytes := []byte(b)
	var combined []byte
	if a < b {
		combined = append(aBytes, bBytes...)
	} else {
		combined = append(bBytes, aBytes...)
	}

	hash := sha256.Sum256(combined)

	// 4 emoji from first 8 bytes (2 bytes per emoji index).
	emojis := make([]string, 4)
	for i := 0; i < 4; i++ {
		idx := int(hash[i*2])
		emojis[i] = emojiTable[idx]
	}
	emoji = strings.Join(emojis, " ")

	// 6-digit numeric code from bytes 8-10 (for accessibility).
	num := int(hash[8])<<16 | int(hash[9])<<8 | int(hash[10])
	num = num % 1000000
	numeric = fmt.Sprintf("%03d-%03d", num/1000, num%1000)

	return emoji, numeric
}

// FingerprintPrefix returns the first 8 hex chars of the SHA-256 fingerprint
// for storage in the verified attribute.
func FingerprintPrefix(a, b peer.ID) string {
	aBytes := []byte(a)
	bBytes := []byte(b)
	var combined []byte
	if a < b {
		combined = append(aBytes, bBytes...)
	} else {
		combined = append(bBytes, aBytes...)
	}

	hash := sha256.Sum256(combined)
	return fmt.Sprintf("sha256:%x", hash[:4])
}
