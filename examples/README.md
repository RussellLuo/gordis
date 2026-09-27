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
| [events](events/README.md) | Typed Topic subscription and publication |
| [isolation](isolation/README.md) | View isolation and multiple providers |
| [ownership](ownership/README.md) | Nested Mount, generation ownership, and recursive cleanup |
| [readiness](readiness/README.md) | Ownership separated from Service readiness |
| [lifecycle](lifecycle/README.md) | Tasks, request leases, entrance withdrawal, and cleanup |
| [dynamic](dynamic/README.md) | Provider removal, Pending transition, and Operation Restore |

## Process extensions

| Example | Focus |
| --- | --- |
| [process](process/README.md) | A typed Service implemented by an external process |
| [process events](process-events/README.md) | A typed Topic published by an external process |
| [duplex](duplex/README.md) | The lower-level bidirectional process protocol |

## Application integration

| Example | Focus |
| --- | --- |
| [application plugin](application-plugin/README.md) | An application-owned package Loader with process backend and UI |
