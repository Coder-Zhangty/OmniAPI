const path = require('node:path');
const { createRequire } = require('node:module');
const deps = path.resolve(__dirname, '../../archive/web/node_modules');
const dep = createRequire(path.join(deps, '../package.json'));
const { createRsbuild } = dep('@rsbuild/core');
const { pluginReact } = dep('@rsbuild/plugin-react');
(async () => {
 const build = await createRsbuild({rsbuildConfig:{root:__dirname,plugins:[pluginReact()],source:{entry:{index:'./src/main.tsx'}},resolve:{alias:{...Object.fromEntries(['react','react-dom','dompurify'].map(n=>[n,path.join(deps,n)])),marked:dep.resolve('marked')}},html:{template:'./template.html'},output:{distPath:{root:path.join(__dirname,'dist'),js:'assets',css:'assets'},assetPrefix:'./',filename:{js:'[name].[contenthash:8].js',css:'[name].[contenthash:8].css'},cleanDistPath:true},splitChunks:{preset:'single-vendor'}}});
 await build.build();
})().catch(e=>{console.error(e);process.exitCode=1});
