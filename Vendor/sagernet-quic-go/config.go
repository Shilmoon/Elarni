package quic

import (
	"fmt"
	"time"

	"github.com/sagernet/quic-go/internal/protocol"
	"github.com/sagernet/quic-go/quicvarint"
)

// Clone clones a Config.
func (c *Config) Clone() *Config {
	copy := *c
	return &copy
}

func (c *Config) handshakeTimeout() time.Duration {
	return 2 * c.HandshakeIdleTimeout
}

func (c *Config) maxRetryTokenAge() time.Duration {
	return c.handshakeTimeout()
}

// initialPacketSize is the size of the first packets: the configured one, or the default.
func (c *Config) initialPacketSize() protocol.ByteCount {
	if c.InitialPacketSize == 0 {
		return protocol.InitialPacketSize
	}
	return protocol.ByteCount(c.InitialPacketSize)
}

// initialPacketSizeIsFloor says if the application set InitialPacketSize as the smallest packet
// size it accepts. The default and Chrome's size fall back to 1200 bytes instead.
func (c *Config) initialPacketSizeIsFloor() bool {
	return c.InitialPacketSize != 0 && !c.ChromeParrot
}

func validateConfig(config *Config) error {
	if config == nil {
		return nil
	}
	const maxStreams = 1 << 60
	if config.MaxIncomingStreams > maxStreams {
		config.MaxIncomingStreams = maxStreams
	}
	if config.MaxIncomingUniStreams > maxStreams {
		config.MaxIncomingUniStreams = maxStreams
	}
	if config.MaxStreamReceiveWindow > quicvarint.Max {
		config.MaxStreamReceiveWindow = quicvarint.Max
	}
	if config.MaxConnectionReceiveWindow > quicvarint.Max {
		config.MaxConnectionReceiveWindow = quicvarint.Max
	}
	if config.InitialPacketSize > 0 && config.InitialPacketSize < protocol.MinInitialPacketSize {
		config.InitialPacketSize = protocol.MinInitialPacketSize
	}
	if config.InitialPacketSize > protocol.MaxPacketBufferSize {
		config.InitialPacketSize = protocol.MaxPacketBufferSize
	}
	// check that all QUIC versions are actually supported
	for _, v := range config.Versions {
		if !protocol.IsValidVersion(v) {
			return fmt.Errorf("invalid QUIC version: %s", v)
		}
	}
	return nil
}

// populateConfig populates fields in the quic.Config with their default values, if none are set
// it may be called with nil
func populateConfig(config *Config) *Config {
	if config == nil {
		config = &Config{}
	}
	versions := config.Versions
	if len(versions) == 0 {
		versions = protocol.SupportedVersions
	}
	handshakeIdleTimeout := protocol.DefaultHandshakeIdleTimeout
	if config.HandshakeIdleTimeout != 0 {
		handshakeIdleTimeout = config.HandshakeIdleTimeout
	}
	idleTimeout := protocol.DefaultIdleTimeout
	if config.MaxIdleTimeout != 0 {
		idleTimeout = config.MaxIdleTimeout
	}
	initialStreamReceiveWindow := config.InitialStreamReceiveWindow
	if initialStreamReceiveWindow == 0 {
		initialStreamReceiveWindow = protocol.DefaultInitialMaxStreamData
	}
	maxStreamReceiveWindow := config.MaxStreamReceiveWindow
	if maxStreamReceiveWindow == 0 {
		maxStreamReceiveWindow = protocol.DefaultMaxReceiveStreamFlowControlWindow
	}
	initialConnectionReceiveWindow := config.InitialConnectionReceiveWindow
	if initialConnectionReceiveWindow == 0 {
		initialConnectionReceiveWindow = protocol.DefaultInitialMaxData
	}
	maxConnectionReceiveWindow := config.MaxConnectionReceiveWindow
	if maxConnectionReceiveWindow == 0 {
		maxConnectionReceiveWindow = protocol.DefaultMaxReceiveConnectionFlowControlWindow
	}
	maxIncomingStreams := config.MaxIncomingStreams
	if maxIncomingStreams == 0 {
		maxIncomingStreams = protocol.DefaultMaxIncomingStreams
	} else if maxIncomingStreams < 0 {
		maxIncomingStreams = 0
	}
	maxIncomingUniStreams := config.MaxIncomingUniStreams
	if maxIncomingUniStreams == 0 {
		maxIncomingUniStreams = protocol.DefaultMaxIncomingUniStreams
	} else if maxIncomingUniStreams < 0 {
		maxIncomingUniStreams = 0
	}
	// Elarni: an unset InitialPacketSize stays zero, as in quic-go/quic-go#5911, so that a size the
	// application set (the smallest it accepts) can be told apart from the default.
	initialPacketSize := config.InitialPacketSize
	enableDatagrams := config.EnableDatagrams
	omitMaxDatagramFrameSize := config.OmitMaxDatagramFrameSize
	if config.ChromeParrot {
		// Chrome always advertises DATAGRAM support, so enable it and never omit
		// the transport parameter; leaving it out would be one parameter short of
		// Chrome's set.
		enableDatagrams = true
		omitMaxDatagramFrameSize = false
		// Chrome pins these, so anything the caller asked for is overridden.
		idleTimeout = chromeMaxIdleTimeout
		initialStreamReceiveWindow = chromeInitialMaxStreamData
		initialConnectionReceiveWindow = chromeInitialMaxData
		maxIncomingStreams = chromeMaxIncomingStreams
		maxIncomingUniStreams = chromeMaxIncomingUniStreams
		initialPacketSize = chromeInitialPacketSize
		// The auto-tuning ceilings must not sit below the starting windows.
		maxStreamReceiveWindow = max(maxStreamReceiveWindow, initialStreamReceiveWindow)
		maxConnectionReceiveWindow = max(maxConnectionReceiveWindow, initialConnectionReceiveWindow)
	}

	return &Config{
		GetConfigForClient:               config.GetConfigForClient,
		Versions:                         versions,
		HandshakeIdleTimeout:             handshakeIdleTimeout,
		MaxIdleTimeout:                   idleTimeout,
		KeepAlivePeriod:                  config.KeepAlivePeriod,
		InitialStreamReceiveWindow:       initialStreamReceiveWindow,
		MaxStreamReceiveWindow:           maxStreamReceiveWindow,
		InitialConnectionReceiveWindow:   initialConnectionReceiveWindow,
		MaxConnectionReceiveWindow:       maxConnectionReceiveWindow,
		AllowConnectionWindowIncrease:    config.AllowConnectionWindowIncrease,
		MaxIncomingStreams:               maxIncomingStreams,
		MaxIncomingUniStreams:            maxIncomingUniStreams,
		TokenStore:                       config.TokenStore,
		EnableDatagrams:                  enableDatagrams,
		OmitMaxDatagramFrameSize:         omitMaxDatagramFrameSize,
		AssumePeerMaxDatagramFrameSize:   config.AssumePeerMaxDatagramFrameSize,
		InitialPacketSize:                initialPacketSize,
		DisablePathMTUDiscovery:          config.DisablePathMTUDiscovery,
		EnableStreamResetPartialDelivery: config.EnableStreamResetPartialDelivery,
		Allow0RTT:                        config.Allow0RTT,
		Tracer:                           config.Tracer,
		MaxDatagramFrameSize:             config.MaxDatagramFrameSize,
		DisablePathManager:               config.DisablePathManager,
		ChromeParrot:                     config.ChromeParrot,
	}
}
