// Package event contains the abstractions for a local event bus, along with the standard events
// that libp2p subsystems may emit.
//
// Source code is arranged as follows:
//   - doc.go: this file.
//   - bus.go: abstractions for the event bus.
//   - rest: event structs, sensibly categorised in files by entity, and following this naming convention:
//     Evt[Entity (noun)][Event (verb past tense / gerund)]
//     The past tense is used to convey that something happened, whereas the gerund form of the verb (-ing)
//     expresses that a process is in progress. Examples: EvtConnEstablishing, EvtConnEstablished.
package event
