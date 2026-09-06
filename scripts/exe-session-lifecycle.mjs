export async function beginDisposableSession({stopOriginal,startFixture,verifyFixture,restore}) {
  try {
    await stopOriginal();
    await startFixture();
    return await verifyFixture();
  } catch(error) {
    try {await restore();} catch(recoveryError) {throw new AggregateError([error,recoveryError],'EXE setup and restoration failed');}
    throw error;
  }
}

export async function finishDisposableSession({closeBrowser,restore,verifyOriginal}) {
  const errors=[];let restored=false;
  try {await closeBrowser();} catch(error) {errors.push({phase:'browser cleanup',message:error.message.split('\n')[0]});}
  try {await restore();await verifyOriginal();restored=true;} catch(error) {errors.push({phase:'EXE restoration',message:error.message.split('\n')[0]});}
  return {restored,errors};
}
