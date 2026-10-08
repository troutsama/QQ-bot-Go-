package qqbot

import (
	"net/http"
	"strings"

	"github.com/tencent-connect/botgo/errs"
)

var quotaMarkers = []string{
	"被动回复",
	"回复次数",
	"频次",
	"frequency",
	"msg_seq",
	"exceed",
}

func IsQuotaExhausted(err error) bool {
	if err == nil {
		return false
	}
	e := errs.Error(err)
	switch e.Code() {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusTooManyRequests:
	default:
		return false
	}
	body := strings.ToLower(e.Text())
	for _, m := range quotaMarkers {
		if strings.Contains(body, strings.ToLower(m)) {
			return true
		}
	}
	return false
}
