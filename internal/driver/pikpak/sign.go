package pikpak

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
)

// captchaSign 计算 captcha/init 的 meta.captcha_sign：
// 起始串 = clientID + clientVersion + packageName + deviceID + 毫秒时间戳，
// 逐条盐做 str = md5hex(str + salt)，结果加版本前缀 "1."。
func captchaSign(clientID, clientVersion, packageName, deviceID string, tsMillis int64, salts []string) string {
	str := clientID + clientVersion + packageName + deviceID + strconv.FormatInt(tsMillis, 10)
	for _, salt := range salts {
		sum := md5.Sum([]byte(str + salt))
		str = hex.EncodeToString(sum[:])
	}
	return "1." + str
}
