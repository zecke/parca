# Parca UI

This is a [Next.js](https://nextjs.org/) project that utilizes
the [static HTML export feature](https://nextjs.org/docs/advanced-features/static-html-export)
of [Next.js](https://nextjs.org/)

## Development

First, run the development server:

```shell
npm run dev
# or
yarn dev
```

Open [http://localhost:3000](http://localhost:3000) with your browser to see the result.

You can start editing the page by modifying `pages/index.tsx`. The page auto-updates as you edit the file.

## Build

To build the UI, you can use `Makefile` at the root of the project to run the following commands.

Run the following command to generate static files:

```shell
make ui # yarn install && yarn export
```

We use [Next.js](https://nextjs.org/) static HTML export and embed artifacts into the final binary distribution.
See https://pkg.go.dev/embed
for further details.

Run following to build the `parca` binary with embedded assets.

```shell
make build
```

### Generate Static files

Run following to generate static assets separately:

```shell
npm run export
# or
yarn export
```

### Development workflow

> Before make sure all the tools you need are installed. The Linux users can simply run `//env.sh`.

You can set up a cluster and all else you need by simply running:
```shell
make dev/up
```

For a simple local development setup we use [Tilt](https://tilt.dev).
```shell
tilt up
```
