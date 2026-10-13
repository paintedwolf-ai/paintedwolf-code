//! Bounded native Linux desktop transport.

mod session_bus;

pub(crate) use session_bus::connect;
