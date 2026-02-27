#!/usr/bin/env node

// Test script to check which API URLs are accessible
const urls = [
  'http://192.168.1.51:7777/api/health',
  'http://172.17.0.1:7777/api/health',
  'http://localhost:7777/api/health',
  'http://127.0.0.1:7777/api/health',
];

async function testUrl(url) {
  try {
    const response = await fetch(url);
    if (response.ok) {
      const data = await response.json();
      console.log(`✅ ${url} - OK (${response.status})`);
      return true;
    } else {
      console.log(`❌ ${url} - Failed (${response.status})`);
      return false;
    }
  } catch (error) {
    console.log(`❌ ${url} - Error: ${error.message}`);
    return false;
  }
}

async function testAllUrls() {
  console.log('🔍 Testing API connectivity...\n');

  for (const url of urls) {
    await testUrl(url);
  }

  console.log('\n💡 If none work, make sure the mediastorm backend is running:');
  console.log('   cd /root/NovaStream/backend && go run main.go');
}

testAllUrls();
