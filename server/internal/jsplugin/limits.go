package jsplugin

import "time"

const (
	probePoolResident = 2
	probePoolMax      = 8
	probeHookTimeout  = 500 * time.Millisecond
	// poolWaitTimeout bounds how long a call queues for a runtime when the pool is at max.
	poolWaitTimeout = time.Second

	protocolPoolResident = 4
	protocolPoolMax      = 16
	// protocolHookTimeout is shorter than the probe timeout: protocol hooks run on the request path.
	protocolHookTimeout = 50 * time.Millisecond
	// signHookTimeout is the budget for signRequest (request shaping and cryptographic signing).
	signHookTimeout = 100 * time.Millisecond
	// streamHookTimeout is the per-event budget for transformStream.
	streamHookTimeout = 5 * time.Millisecond
	// scheduleHookTimeout is the budget for periodic onSchedule calls.
	scheduleHookTimeout = time.Second
	// MaxProtocolResponseBody is the largest upstream body passed to parseResponse.
	MaxProtocolResponseBody = 4 << 20
	// MaxProbeSteps is the number of probe hook calls, which allows at most five HTTP requests.
	MaxProbeSteps = 6
	// MaxProbeRequestBody is the encoded size of one probe request body.
	MaxProbeRequestBody  = 64 << 10
	maxProbeResponseBody = 1 << 20
	// MaxAllocBytes is the per-call allocation budget passed to moejs.
	MaxAllocBytes int64 = 32 << 20
	// MaxResultBytes is the hook result bound passed to moejs.
	MaxResultBytes        int64 = 1 << 20
	maxRequestHeaders           = 32
	maxRequestHeaderBytes       = 8 << 10
	maxPluginLogs               = 50
	maxPluginLogBytes           = 2048
	hostAPIVersion              = 2
	hookAPIVersion              = 1

	MaxStorageKeysPerScope  = 64
	MaxStorageBytesPerScope = 64 << 10
	maxBinaryInputBytes     = 4 << 20
	maxRandomBytes          = 64 << 10
)

// HeapLimitAvailable reports whether NewRuntime is configured with both an
// allocation budget and a result-size bound. A zero value means that limit is off.
func HeapLimitAvailable() bool {
	return MaxAllocBytes > 0 && MaxResultBytes > 0
}
