import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Node global (the project's tsconfig references @types/node but it isn't
// installed); declared locally so this config type-checks without a new dep.
declare const process: { env: Record<string, string | undefined> };

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
			},
			// Kit 3 dropped the built-in $lib alias; keep it rather than rename every import.
			alias: { $lib: 'src/lib' },
			// SPA: a single fallback page; all routing happens client-side and data
			// comes from the Go API. Output is written into the Go server's embed dir.
			adapter: adapter({
				fallback: 'index.html',
				pages: '../internal/httpapi/webdist',
				assets: '../internal/httpapi/webdist'
			})
		})
	],
	// During `vite dev`, proxy API + SSE + image calls to the Go backend.
	// Override the target with SEEURCHIN_API_PROXY (e.g. to point at a running
	// container on :5858 instead of a local `go run` on :5859).
	server: {
		proxy: {
			'/api': {
				target: process.env.SEEURCHIN_API_PROXY || 'http://localhost:5859',
				changeOrigin: true
			}
		}
	}
});
