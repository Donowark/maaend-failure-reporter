package main

import (
	"bytes"
	"encoding/binary"
)

// GetExtendedTcpTable returns a count and MIB_TCPROW_OWNER_PID records.
// LocalPort is stored in network byte order inside a DWORD.
func loopbackListenerPIDs(table []byte, port int) []uint32 {
	const rowSize = 24
	if len(table) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(table))
	if count > (len(table)-4)/rowSize {
		return nil
	}
	var pids []uint32
	for i := 0; i < count; i++ {
		row := table[4+i*rowSize:]
		if binary.LittleEndian.Uint32(row) != 2 || int(binary.BigEndian.Uint16(row[8:10])) != port {
			continue
		}
		if !bytes.Equal(row[4:8], []byte{127, 0, 0, 1}) && !bytes.Equal(row[4:8], []byte{0, 0, 0, 0}) {
			continue
		}
		pids = append(pids, binary.LittleEndian.Uint32(row[20:24]))
	}
	return pids
}
