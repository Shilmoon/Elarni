# Elarni patches

Upstream: github.com/sagernet/quic-go v0.61.0-sing-box-mod.6 (revision
f7b7e9c4c6f6ec45e4ab2b4788eca63192975d80, the version sing-quic's TUIC client
requires), joined to the build by `go.work`. Only the non-test sources are
copied, as in `Vendor/quic-go`. SagerNet's mod.7, mod.12 and mod.13 commits are
taken on top (below), without the commits between them.

- `connection.go`, `framer.go` (`blockedStreams`, `HasUnblockedStreamData`),
  `internal/ackhandler/{interfaces,sent_packet_handler}.go` (`UpdateAppLimited`),
  `internal/congestion/cubic_sender.go` (`OnPacketSent`): SagerNet/quic-go
  v0.61.0-sing-box-mod.7 (0cae1a7786), mod.12 (80c132feb2) and mod.13 (2e73222e), in that
  order, as upstream ships them (sing-box 1.14.4 runs mod.12). They rework how
  application-limited periods reach a congestion controller installed with
  `SetCongestionControl` (sing-quic's BBR for TUIC links that ask for it; sing-box#4538 reports
  upload at about 60% of a fresh connection's after downloads on the same connection), and the
  cubic pacer no longer spends its budget on ACK-only packets. None changes the public
  `congestion` package, so sing-quic v0.7.1 stays. Xray-core's
  `proxy/tuic/bbr_upload_test.go` (`BenchmarkBBRUploadAfterDownload`) follows the issue's
  reproduction over a loopback relay shaped to 100 Mbit/s and 5 ms each way; it does not show
  the drop there, before or after these commits, so it does not demonstrate the fix.

- `flow_controller_connection.go`: the `Vendor/quic-go` change (received stream
  bytes count as read for the connection window, up to twice its maximum size),
  so downloads the application stopped reading no longer freeze every other
  stream of a TUIC connection: 8 stalled streams at quic-go's first 2 MiB stream
  window filled the client's 16 MiB connection window. Past twice the window of
  unread bytes new streams still wait, which bounds memory.
  Test: `flow_controller_stalled_test.go`; end to end in Xray-core's
  `proxy/tuic/stalled_streams_test.go`.
  On iOS the allowance is half the maximum window instead of twice it (`unreadAllowance`), so
  one TUIC connection holds at most 24 MiB unread instead of 48 MiB: iOS kills the packet
  tunnel above about 50 MB. Test: `TestUnreadAllowanceIsSmallerOnIOS`.
- `client.go` (`setupTransportConn`, `DialConn`, `DialEarlyConn`): a conn with a
  `PacketConn() net.PacketConn` method that returns non-nil gets quic-go's unconnected-socket
  path on that socket (batched receive, GSO, ECN, DF). Xray's UDP dialer returns an unconnected
  socket in a wrapper, which hid it from quic-go; the connected path a syscall.Conn gets sends
  without an address, which that socket rejects. A conn with a `SetQUICConn(*Conn)` method
  learns the connection dialed over it, which sing-quic's TUIC client keeps private; Xray-core
  checks it after a network change. Tests: `client_unconnected_test.go`; end to end in
  Xray-core's `proxy/tuic/path_test.go`.
- `send_queue.go`, `connection.go` (`handshakeCompleteForSend`), `sys_conn_netchange_{unix,windows,other}.go`:
  a send that fails with ENETUNREACH, EHOSTUNREACH, EADDRNOTAVAIL or ENETDOWN (the interface,
  route or local address gone while the device moves between Wi-Fi and cellular; WSAE* on
  Windows) drops that packet instead of closing the connection, as upstream's ENOBUFS handling
  (e86a8a79) does, but only once the handshake is complete: the connection sets the flag in
  `handleHandshakeComplete`, and before that these errors still fail the dial at once (an IPv6
  server address without an IPv6 route). Loss recovery resends the packet, and the idle timeout
  still ends a connection whose network does not come back. Test: `send_queue_transient_test.go`.
- `send_stream.go` (`write`): quic-go/quic-go 254a5393 (issue #6136). A Write blocked when the
  peer sent STOP_SENDING (or the stream was canceled) put its data into a new frame that was
  never sent, so the stream never completed and its MAX_STREAMS credit never came back. Test:
  `send_stream_reset_test.go`.
- `crypto_stream.go`, `frame_sorter.go` (`ReadPos`), `internal/protocol/params.go`
  (`MaxCryptoStreamBufferSize`): quic-go/quic-go PR #6265 (5ee23131). The 16 KiB limit applies to
  out-of-order CRYPTO data past the read position, not to the stream offset, so a server flight
  over 16 KiB (long certificate chains, post-quantum key shares) no longer fails the handshake
  with CRYPTO_BUFFER_EXCEEDED. Test: `crypto_stream_limit_test.go`.
- `internal/ackhandler/received_packet_history.go`: quic-go/quic-go PR #6308 (48250ad6). Past 64
  ACK ranges the oldest one is dropped through `DeleteBelow`, so a late duplicate of a packet in
  it is rejected instead of processed again (a DATAGRAM delivered twice). Test:
  `internal/ackhandler/received_packet_history_limit_test.go`.
- `connection.go` (`sendPacketsWithoutGSO`, `sendPacketsWithGSO`): quic-go/quic-go PR #6223
  (5a6c94ec) as is, as in `Vendor/quic-go`: when the send queue frees up before the run loop
  checks it, sending goes on at once instead of waiting for the next timer or packet. Test:
  `send_queue_freed_test.go`.
- `framer.go` (`AppendControlFrames`, `AppendStreamFrames`), `packet_packer.go`
  (`composeNextPacket`): quic-go/quic-go PR #5970 (6dfb89eb), ported by hand as in
  `Vendor/quic-go`: frames are packed in the order ACK, control frames, retransmissions,
  DATAGRAM, STREAM, so UDP over TUIC no longer delays MAX_DATA, MAX_STREAM_DATA and lost frames.
  Only the order is taken (one DATAGRAM per packet, with its length; the `peekTimes` limit is
  kept). Test: `packet_packer_datagram_order_test.go`.
- `connection.go` (`writeUntilInitialPacketSizeTried`, `InitialPacketSize`, `maxMTU`),
  `config.go` (`initialPacketSize`, `initialPacketSizeIsFloor`), `interface.go`,
  `internal/congestion/cubic_sender.go`, `internal/ackhandler/sent_packet_handler.go`
  (`SetMaxDatagramSize`): quic-go/quic-go PR #5911 (99c74363), ported by hand as in
  `Vendor/quic-go` (see there): a "message too long" error for a packet over 1,200 bytes
  during the handshake drops the connection to 1,200 bytes, which `InitialPacketSize()` then
  reports (sing-quic's TUIC congestion controllers are created with it). A controller installed
  with `SetCongestionControl` is told only sizes above the configured initial size: with
  `ZeroRTTHandshake` sing-quic installs one on the early connection, possibly before the
  fallback, and its controllers panic on a smaller size. Tests: `mtu_fallback_test.go`,
  `internal/ackhandler/installed_congestion_size_test.go`.
- `go.mod`, `go.sum`: tidied for the copied sources, without the `tool` block.
