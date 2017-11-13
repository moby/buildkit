# Design: distributed mode (work in progress)

## master
- Stateless.
-- No etcd.
-- The orchestrator (Kubernetes) is expected to restart master container on failure.

## worker
- Workers could be started by passing the master host information.
-- A worker connect to the master and tell its workerID.
-- TODO: how to connect to the master with multiple container replicas?

## scheduling
- The master asks the workers: "are you able to reproduce the cachekey for this operation?", and the master become aware of `map[op][]workerID`.
-- This map does not need to be 100% accurate. So we have many opportunity for optimization.
-- The master could ask cpu/mem/io stat as well and utilize such information for better scheduling.
- The master schedules a vertex job to a worker which is likely to have the most numbers of the dependee-vertice caches.
-- If none have it, choose randomly

## scalability
- We may be able to use gossip (or something like that) for improving scalability of the cache map

## credential
-  Because the worker can need credentials at any time, and it does not have any open session with the client, it needs to ask the master who does have an open session with the client

## local file
- we need to find the same worker that already received the content from client the first time.
- OR always sync to manager: manager can do the work of a worker (with caveat of constraints)
