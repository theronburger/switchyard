package processhost

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

func fingerprintCommand(executable string, arguments []string) string {
	hash := sha256.New()
	writeFingerprintPart := func(value string) {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(value))
	}
	writeFingerprintPart(executable)
	for _, argument := range arguments {
		writeFingerprintPart(argument)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
