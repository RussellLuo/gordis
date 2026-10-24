# Gordis examples

English | [中文](README.zh.md)

Each example focuses on one part of the plugin runtime. Start with `basic`,
then follow the sections below as the corresponding concepts become relevant.
The linked README in each directory contains run instructions, expected output,
and an explanation of the implementation.

## Core concepts

| Example | Focus |
| --- | --- |
| [basic](basic/README.md) | Typed services, JSON configuration, and multiple instances |
| [isolation](isolation/README.md) | View isolation and multiple providers |
| [ownership](ownership/README.md) | Nested Mount, generation ownership, and recursive cleanup |
| [readiness](readiness/README.md) | Ownership separated from Service readiness |
| [lifecycle](lifecycle/README.md) | Tasks, request leases, entrance withdrawal, and cleanup |
| [dynamic](dynamic/README.md) | Provider removal, Pending transition, and Operation Restore |
| [optional service](optional-service/README.md) | Request-time Lookup and database fallback without consumer restarts |

## Optional extensions

| Example | Focus |
| --- | --- |
| [events](events/README.md) | Typed Topic subscription and publication |
| [RPC](../rpc/examples/README.md) | Typed remote Services, nested callbacks, and streaming results |
| [RPC events](../rpc/examples/events/README.md) | Host Request and Child Notice with complete round-trip waiting |
