// Resolve tooling exclusively from this fixture package's pinned dependencies.
require('ts-node').register({
  transpileOnly: true,
  skipProject: true,
  compilerOptions: {
    module: 'commonjs',
    target: 'es2022',
    moduleResolution: 'node',
  },
});
