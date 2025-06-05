// build.mjs
import { build } from 'esbuild';

build({
  entryPoints: ['src/index.ts'],
  bundle: true,
  minify: true,
  outfile: 'dist/index.js',
  format: 'esm',
  platform: 'node',
  external: [
    'onnxruntime-node',
    'sharp',
    '@xenova/transformers',
    '@lancedb/lancedb',
    '@lancedb/lancedb-win32-x64-msvc',
    'path',
    'fs',
    'os',
    'http',
    'apache-arrow'
  ],
  target: ['esnext'],
}).then(() => {
  console.log('✅ Build succeeded.');
}).catch((err) => {
  console.error('❌ Build failed:', err);
  process.exit(1);
});
