import fs from 'node:fs';
import path from 'node:path';
const root=path.resolve(process.argv[2] || 'console');
const output=path.resolve(process.argv[3] || '/tmp/agents-frontend-licenses');
fs.mkdirSync(output,{recursive:true});
const packages=JSON.parse(fs.readFileSync(path.join(root,'package-lock.json'),'utf8')).packages;
const inventory=[];
for (const [location,entry] of Object.entries(packages)) {
 if (!location || !location.startsWith('node_modules/') || entry.dev) continue;
 const directory=path.join(root,location);
 if (!fs.existsSync(directory)) continue;
 const metadata=JSON.parse(fs.readFileSync(path.join(directory,'package.json'),'utf8'));
 const destination=path.join(output,'modules',metadata.name);
 fs.mkdirSync(destination,{recursive:true});
 for (const name of fs.readdirSync(directory)) {
  if (/^(licen[cs]e|copying|notice)([._-]|$)/i.test(name) && fs.statSync(path.join(directory,name)).isFile()) {
   fs.copyFileSync(path.join(directory,name),path.join(destination,name));
  }
 }
 inventory.push({name:metadata.name,version:metadata.version,license:metadata.license || 'See package notices'});
}
for (const name of ['THIRD_PARTY_NOTICES.md','SHADCN_LICENSE','LICENSE.shadcn']) {
 if (fs.existsSync(path.join(root,name))) fs.copyFileSync(path.join(root,name),path.join(output,name));
}
if (fs.existsSync(path.join(root,'licenses'))) {
 fs.cpSync(path.join(root,'licenses'),path.join(output,'vendored'),{recursive:true});
}
fs.writeFileSync(path.join(output,'packages.json'),JSON.stringify(inventory,null,2)+'\n');
