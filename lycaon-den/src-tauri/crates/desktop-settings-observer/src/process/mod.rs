//! Owned process lifetime for one fixed observer image, with no action requests.
mod image;
use crate::{read,Frame,Status};
use std::{io::{self,BufReader},path::Path,process::{Child,Command,Stdio},sync::{Arc,atomic::{AtomicBool,Ordering},mpsc},thread,time::{Duration,Instant}};
const ENVIRONMENT: [&str; 6] = ["DBUS_SESSION_BUS_ADDRESS","XDG_RUNTIME_DIR","LANG","LC_ALL","LC_CTYPE","TZ"];
fn command(image: &image::Image) -> Command {
    let mut command=Command::new(image.executable());
    command.env_clear().stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::null());
    for key in ENVIRONMENT { if let Some(value)=std::env::var_os(key) {command.env(key,value);} }
    command
}
fn stop(child: &mut Child) {
    drop(child.stdin.take());
    let deadline=Instant::now()+Duration::from_secs(2);
    loop {
        match child.try_wait() { Ok(Some(_))=>return, Err(_)=>break, Ok(None)=>{} }
        if Instant::now()>=deadline {break;}
        thread::sleep(Duration::from_millis(20));
    }
    let _=child.kill();let _=child.wait();
}
fn one(shell:&Path,stop_requested:&AtomicBool,mut observe:impl FnMut(Frame)) -> io::Result<Status> {
    let image=image::Image::open(shell)?;
    let mut child=command(&image).spawn()?;
    let stdout=child.stdout.take().ok_or_else(||io::Error::other("settings observer pipe unavailable"))?;
    let (sender,receiver)=mpsc::sync_channel(1);
    let reader=thread::spawn(move || {
        let mut input=BufReader::new(stdout);let mut sequence=0;
        loop {
            let result=read(&mut input,sequence);
            if let Ok(Some(frame))=&result {sequence=frame.sequence;}
            let done=!matches!(&result,Ok(Some(_)));
            if sender.send(result).is_err() || done {break;}
        }
    });
    let mut result=Status::Disconnected;let mut last=Instant::now();let mut started=false;
    loop {
        if stop_requested.load(Ordering::Acquire) {break;}
        match receiver.recv_timeout(Duration::from_millis(100)) {
            Ok(Ok(Some(frame)))=> {last=Instant::now();result=frame.status;started=true;observe(frame);if result!=Status::Ready {break;}}
            Ok(Err(_))=> {result=Status::Failed;break;}
            Ok(Ok(None))|Err(mpsc::RecvTimeoutError::Disconnected)=>break,
            Err(mpsc::RecvTimeoutError::Timeout)=>{},
        }
        if last.elapsed()>Duration::from_secs(if started {30}else{5}) {result=Status::TimedOut;break;}
        if matches!(child.try_wait(),Ok(Some(_))) {break;}
    }
    // Drop the bounded receiver before joining a reader blocked in send.
    drop(receiver);stop(&mut child);let _=reader.join();Ok(result)
}
pub fn watch(shell:&Path,stop_requested:Arc<AtomicBool>,mut observe:impl FnMut(Frame),mut status:impl FnMut(Status)) {
    for attempt in 0..5 {
        if stop_requested.load(Ordering::Acquire) {return;}
        let outcome=one(shell,&stop_requested,&mut observe).unwrap_or_else(|error|if error.kind()==io::ErrorKind::NotFound {Status::Missing}else{Status::Failed});
        if stop_requested.load(Ordering::Acquire) {return;}
        status(outcome);
        if matches!(outcome,Status::Denied|Status::Failed) {return;}
        let deadline=Instant::now()+Duration::from_secs(1<<attempt);
        while !stop_requested.load(Ordering::Acquire) && Instant::now()<deadline {thread::sleep(Duration::from_millis(100));}
    }
    status(Status::Failed);
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test] fn environment_never_inherits_parent_credentials() {
        assert!(!ENVIRONMENT.iter().any(|name|name.contains("TOKEN")||name.contains("KEY")||name.contains("PASSWORD")));
        assert_eq!(ENVIRONMENT.len(),6);
    }
    #[test] fn foreign_or_missing_image_cannot_select_a_command() {
        assert!(image::Image::open(Path::new("/nonexistent/bin/painted-wolf-code")).is_err());
        assert!(image::Image::open(Path::new("/bin/sh")).is_err());
    }
}
