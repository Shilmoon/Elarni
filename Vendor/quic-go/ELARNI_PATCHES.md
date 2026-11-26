# Elarni patches

Upstream: github.com/apernet/quic-go v0.61.1-0.20260806010916-184d081eef3e (the
version Vendor/Xray-core requires), joined to the build by `go.work`. Only the
non-test sources are copied; the example, interop, integration-test, test-utility
and mock directories are left out.

- `flow_controller_connection.go`: bytes that arrived on a stream count as read for
  the connection window, up to twice its maximum size. Before, bytes held unread by
  streams the application stopped reading counted against the connection until it
  was consumed, so three stalled downloads froze every other stream of a Hysteria2
  or XHTTP/3 connection, with or without the Chrome parrot (each stream may hold
  6-8 MiB of a 15-20 MiB connection window). Each stream's own window still bounds
  what it holds, and the window update rides on the ACKs the stalled data elicits.
  Test: `flow_controller_stalled_test.go`; end to end in Xray-core's
  `transport/internet/hysteria/stalled_streams_test.go` and
  `transport/internet/splithttp/h3_stall_test.go`.
  On iOS the allowance is half the maximum window instead of twice it (`unreadAllowance`), so
  one XHTTP/3 connection holds at most 22.5 MiB unread instead of 45 MiB: iOS kills the packet
  tunnel above about 50 MB. Test: `TestUnreadAllowanceIsSmallerOnIOS`.
- `http3/transport.go` (`getClient`): the dial that every request for a host waits on no longer
  ends when the request that started it is canceled; `Transport.Close` still ends it. One closed
  app aborted a cold XHTTP/3 handshake for every flow waiting on it. Test: Xray-core's
  `TestXHTTP3CanceledFirstFlowKeepsTheSharedDial`.
- `connection.go`: `Conn.Ping` queues a PING, which the peer acknowledges at once; XHTTP/3
  checks its connections this way after a network change (`TestPathChangeKeepsAnsweringConnections`).
- `send_queue.go`, `connection.go` (`handshakeCompleteForSend`), `sys_conn_nobufs_{unix,windows,other}.go`,
  `sys_conn_netchange_{unix,windows,other}.go`: a send that fails with ENOBUFS (a full socket or
  interface queue, common on macOS and iOS) drops that packet instead of closing the connection.
  So does a send that fails with ENETUNREACH, EHOSTUNREACH, EADDRNOTAVAIL or ENETDOWN (the
  interface, route or local address gone while the device moves between Wi-Fi and cellular;
  WSAE* on Windows), but only once the handshake is complete: the connection sets the flag in
  `handleHandshakeComplete`, and before that these errors still fail the dial at once (an IPv6
  server address without an IPv6 route). The packet is already registered with the sent packet
  handler, so loss recovery resends it, and the idle timeout still ends a connection whose
  network does not come back. The ENOBUFS files are SagerNet/quic-go e86a8a79 as is.
  Test: `send_queue_transient_test.go`.
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
- `go.mod`, `go.sum`: tidied for the copied sources, without the `tool` block, so
  the workspace does not pull upstream's test and generator dependencies.
- `http3/{client,headers,request_writer,response_writer,server_conn}.go`: imports
  sorted by gofmt (upstream's module rename left them out of order).
