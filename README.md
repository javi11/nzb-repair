# nzb-repair

A tool to repair incomplete NZB files by reuploading the missing articles using the par2 repair.

## Usage

1. Create a config file `config.yaml` with your Usenet provider details:

```yaml
download_providers:
  - host: news.example.com
    port: 563
    username: your_username
    password: your_password
    connections: 10
    tls: true
upload_providers:
  - host: upload.example.com
    port: 563
    username: your_username
    password: your_password
    connections: 5
    tls: true
```

2. Run the repair tool:

```sh
nzbrepair -c config.yaml path/to/your.nzb
```

Options:

- `-c, --config`: Config file path (required)
- `-o, --output`: Output file path for the repaired nzb (optional)
- `-v, --verbose`: Enable verbose logging (optional)

## Development Setup

To set up the project for development, follow these steps:

1. Clone the repository:

```sh
git clone https://github.com/javi11/nntpcli.git
cd nntpcli
```

2. Install dependencies:

```sh
go mod download
```

3. Run tests:

```sh
make test
```

4. Lint the code:

```sh
make lint
```

5. Generate mocks and other code:

```sh
make generate
```

## Contributing

Contributions are welcome! Please open an issue or submit a pull request. See the [CONTRIBUTING.md](CONTRIBUTING.md) file for details.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
