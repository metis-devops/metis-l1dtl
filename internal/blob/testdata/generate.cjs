// Optional fixture regeneration: node internal/blob/testdata/generate.cjs
// Run npm ci in this directory once; production builds and Go tests need no Node.js.
const path = require('path');
const fs = require('fs');
require('./register.cjs');
const {Blob}=require('./reference/blob');
const {ethers}=require('ethersv6');
const zlib=require('zlib');
const {parseFrames}=require('./reference/frame');
const {Channel,batchReader}=require('./reference/channel');
const {handleEventsSequencerBatchInbox}=require('./reference/inbox');
(async()=>{
 const wallet=new ethers.Wallet('0x'+'01'.repeat(32));
 const to='0x'+'55'.repeat(20);
 const signed=[];
 for(let n=0;n<2;n++) signed.push(ethers.Transaction.from(await wallet.signTransaction({type:0,chainId:1088,nonce:n,gasPrice:7,gasLimit:50000,to,value:n+1,data:'0x1234'})));
 const buf=x=>Buffer.from(x.slice(2),'hex');
 const uv=n=>{let v=BigInt(n),a=[];do{let b=Number(v&127n);v>>=7n;a.push(b|(v?128:0))}while(v);return Buffer.from(a)};
 const sigs=signed.flatMap(t=>[buf(t.signature.r),buf(t.signature.s)]);
 const packed=Buffer.concat([uv(1000),Buffer.alloc(40),uv(2),Buffer.from([0]),uv(50),uv(51),uv(1700000000),uv(1700000001),uv(1),uv(1),uv(0),uv(0),Buffer.from([0,signed[0].signature.yParity|(signed[1].signature.yParity<<1)]),...sigs,buf(to),buf(to),...signed.map(t=>buf(ethers.encodeRlp([ethers.toBeHex(t.value),ethers.toBeHex(t.gasPrice),t.data]))),uv(0),uv(1),uv(50000),uv(50000),Buffer.from([3,2,0]),...Array.from({length:4},(_,i)=>buf(ethers.toBeHex(i+1,32))),buf('0x'+'66'.repeat(20))]);
 const compressed=zlib.deflateSync(buf(ethers.encodeRlp('0x01'+packed.toString('hex'))));
 const frameHeader=Buffer.alloc(23);frameHeader.fill(9,1,17);frameHeader.writeUInt32BE(compressed.length,19);
 const framed=Buffer.concat([frameHeader,compressed,Buffer.from([1])]);
 const logical=Buffer.alloc(130048);logical[0]=0;logical.writeUIntBE(framed.length,1,3);framed.copy(logical,4);
 const encoded=Buffer.alloc(131072);let pos=0;
 for(let round=0;round<1024;round++){
  const a=logical.subarray(pos,pos+=31),x=logical[pos++],b=logical.subarray(pos,pos+=31),y=logical[pos++],c=logical.subarray(pos,pos+=31),z=logical[pos++],d=logical.subarray(pos,pos+=31);
  const headers=[x&63,(y&15)|((x&192)>>2),z&63,((z&192)>>2)|((y&240)>>4)];
  [a,b,c,d].forEach((v,j)=>{const o=round*128+j*32;encoded[o]=headers[j];v.copy(encoded,o+1)});
 }
 const l1BlobData='0x'+encoded.toString('hex');
 const frames=parseFrames(new Blob(l1BlobData).toData(),0);
 const channel=new Channel(Buffer.from(frames[0].id).toString('hex'),0);
 for(const frame of frames)channel.addFrame(frame);
 const read=await batchReader(channel.reader()); const decoded=await read();const span=await decoded.inner.derive(1088n);
 const hash='0x'+'11'.repeat(32), blockHash='0x'+'22'.repeat(32), sender='0x'+'33'.repeat(20);
 const calldata=Buffer.concat([Buffer.from([3,0]),Buffer.from((7n).toString(16).padStart(64,'0'),'hex'),Buffer.from(BigInt(span.l2StartBlock).toString(16).padStart(64,'0'),'hex'),Buffer.from(span.batches.length.toString(16).padStart(8,'0'),'hex'),Buffer.from(hash.slice(2),'hex')]);
 const extra={batchIndex:7,batchRoot:blockHash,batchSize:span.batches.length,prevTotalElements:span.l2StartBlock-1,batchExtraData:'',blockNumber:50,timestamp:100,submitter:sender,l1TransactionHash:'0x'+'44'.repeat(32),l1TransactionData:'0x'+calldata.toString('hex'),context:{inboxBlobSenderAddress:sender}};
 const options={l2ChainId:1088,batchInboxAddress:sender,l1RpcProvider:{getBlockNumber:async()=>50,getTransactionReceipt:async()=>({blockNumber:50,blockHash,index:0}),getBlock:async()=>({timestamp:100,prefetchedTransactions:[{hash,type:3,from:sender,to:sender,index:0,blobVersionedHashes:[hash]}]})},l1BeaconProvider:{getBlobs:async()=>[new Blob(l1BlobData).toData()]}};
 const result=await handleEventsSequencerBatchInbox.parseEvent({},extra,1088,1,options);
 fs.writeFileSync(path.join(__dirname,'typescript-blob.hex'),l1BlobData+'\n');
 fs.writeFileSync(path.join(__dirname,'typescript-blocks.json'),JSON.stringify(result,null,2)+'\n');
 console.log({blocks:result.blockEntries.length,first:span.l2StartBlock,transactions:result.blockEntries.reduce((n,b)=>n+b.transactions.length,0)});
})().catch(err=>{console.error(err);process.exitCode=1});
