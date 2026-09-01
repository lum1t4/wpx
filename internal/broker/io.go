package broker

import "io"

func ioLimitReader(reader io.Reader, limit int64) io.Reader {
	return io.LimitReader(reader, limit)
}
