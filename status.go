package link

import (
    "errors"
)

var (
    SYN        = errors.New("SYN")
    ESTAB      = errors.New("ESTAB")
    FIN_WAIT   = errors.New("FIN_WAIT")
    CLOSE_WAIT = errors.New("CLOSE_WAIT")
    CLOSED     = errors.New("CLOSED")
    RST        = errors.New("RST")

    LowLevelErr = errors.New("low level stream error")
)
