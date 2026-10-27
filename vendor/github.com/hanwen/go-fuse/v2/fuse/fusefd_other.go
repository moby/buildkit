//go:build !linux

package fuse

// OSX and FreeBSD has races when multiple routines read
// from the FUSE device: on unmount, sometime some reads
// do not error-out, meaning that unmount will hang.
const useSingleReader = true

func (r *fuseFD) write(req *request) Status {
	if req.outPayloadSize() == 0 {
		err := handleEINTR(func() error {
			_, err := r.writevFD([][]byte{req.outHeaderBuf, req.outDataBuf})
			return err
		})
		return ToStatus(err)
	}

	if req.readResult != nil {
		req.outPayload, req.status = req.readResult.Bytes(req.outPayload)
		req.serializeHeader(len(req.outPayload))
	}

	_, err := r.writevFD([][]byte{req.outHeaderBuf, req.outDataBuf, req.outPayload})
	if req.readResult != nil {
		req.readResult.Done()
		req.readResult = nil
	}
	return ToStatus(err)
}
