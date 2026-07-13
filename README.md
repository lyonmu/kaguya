<p align="center">
<img src="images/kaguya.png" alt="icon" width="120"/>
</p>

<p align="center">AI Agent With Go</p>
<p align="center">

</p>
<p align="center">
  <a href="README.md">English</a> |
  <a href="README.zh.md">简体中文</a>
</p>

---

# kaguya

**kaguya** is an experimental Go project for building AI Agent applications.

## Who is Kaguya?

**Kaguya** comes from **Kaguya-hime / 辉夜姬**.

In Japanese classical imagery, Kaguya-hime is the moon princess: quiet, elegant, distant, and mysterious.

In _Dragon Raja_（《龙族》）, **Kaguya** is also associated with the Japanese branch's super artificial intelligence system — a calm, rational, and powerful intelligence hidden behind the organization.

This project borrows that image.

`kaguya` is designed to be a quiet, rational, and controllable Agent core.

## About This Project

`kaguya` is a Go-native project for exploring how AI Agents are designed and implemented.

The project focuses on the basic structure of an Agent, including model abstraction, prompt handling, tool calling, and execution flow.

It is currently in early development and is mainly used for learning, experimentation, and gradual framework design.

## Development Testing

Run the default test suite (no network required):

```text
go test ./...
```

Run the integration tests (requires network access and a valid model provider):

```text
go test -tags=integration ./...
```

The integration tests require an uncommitted local `config.yml` file at the repository root. This file is gitignored and must never be committed.

API keys and other credentials should only be stored in the gitignored `config.yml` file or in a controlled secrets management system. Never hard-code secrets in source files, tests, or documentation.
