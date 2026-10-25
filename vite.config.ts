import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  base: '/assets/ui/',
  plugins: [tailwindcss()],
  publicDir: false,
  build: {
    outDir: 'internal/assets/dist/ui',
    emptyOutDir: true,
    rolldownOptions: {
      input: 'ui/src/app.ts',
      output: {
        entryFileNames: 'app.js',
        assetFileNames: '[name][extname]',
      },
    },
  },
});
